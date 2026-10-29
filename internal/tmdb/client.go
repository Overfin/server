package tmdb

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/overfin/server/internal/textclean"
	"github.com/overfin/server/internal/upstream"
)

// Tokens: Overfin uses TMDB's "API Read Access Token", sent as a bearer
// (TMDB's shorter "API key" would travel in URLs, so it isn't taken). Each
// operator brings their own, so one installation's use never puts
// another's at risk.
var readToken = regexp.MustCompile(`^[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+$`)

// ValidToken reports whether token looks like a TMDB read access token.
func ValidToken(token string) bool {
	return len(token) >= 40 && len(token) <= 1000 && readToken.MatchString(token)
}

// BaseURL is TMDB's API.
const BaseURL = "https://api.themoviedb.org/3"

// maxResponse bounds every response read from TMDB.
const maxResponse = 4 << 20

// Client talks to TMDB. Every call is a typed method (SECURITY.md rule
// 1), the key in a header, never in a URL; it never follows redirects,
// bounds response sizes and times out (rule 6).
type Client struct {
	base  *url.URL
	token string
	http  *http.Client
	// slow holds requests back while TMDB asked to slow down; copies of
	// the client share it.
	slow *gate
	// Language, when set, is the language TMDB answers in, ISO 639-1 or
	// regional ("fr", "fr-CA"): titles, overviews, and the text on posters
	// and logos. Asked in a regional one, TMDB answers with that region's
	// translation only: where there's none, a title comes with its
	// original name and no overview, so those are asked again in the
	// language alone (see baseLanguage).
	Language func() string
	// Languages are the server's languages (ISO 639-1, regional or not),
	// the default first: a title without a logo in the language, nor one
	// without text, takes one in these, in order, then one in its original
	// language, before none (its name shown as text). nil for none.
	Languages func() []string
}

// language is the language to ask for; "" for TMDB's default (English).
func (c *Client) language() string {
	if c.Language == nil {
		return ""
	}
	if l := c.Language(); languageCode.MatchString(l) {
		return l
	}
	return ""
}

// languageCode is what an ISO 639-1 code, regional or not, looks like.
var languageCode = regexp.MustCompile(`^[a-z]{2}(-[A-Z]{2})?$`)

// withLanguage adds the language to a query.
func (c *Client) withLanguage(q url.Values) url.Values {
	if q == nil {
		q = url.Values{}
	}
	if l := c.language(); l != "" {
		q.Set("language", l)
	}
	return q
}

// baseLanguage is the language without its region ("fr" for "fr-CA"), to fill
// what the region's translation lacks; "" when the language has no region.
func (c *Client) baseLanguage() string {
	l, _, regional := strings.Cut(c.language(), "-")
	if !regional {
		return ""
	}
	return l
}

// inBase is a copy of a query, in the language without its region.
func (c *Client) inBase(q url.Values) url.Values {
	out := url.Values{}
	for k, v := range q {
		out[k] = v
	}
	out.Set("language", c.baseLanguage())
	return out
}

// lacks says whether a title, in a regional language, came without that
// region's translation: its original name though it's in another
// language, or no overview.
func (r rawTitle) lacks(base string) bool {
	name, original := r.Title, r.OriginalTitle
	if r.Name != "" {
		name, original = r.Name, r.OriginalName
	}
	return r.Overview == "" || (name == original && r.OriginalLanguage != base)
}

// fill takes what the region's translation lacked from the language's.
func (r *rawTitle) fill(b rawTitle) {
	if r.Overview == "" {
		r.Overview = b.Overview
	}
	if r.Title == r.OriginalTitle && b.Title != "" {
		r.Title = b.Title
	}
	if r.Name == r.OriginalName && b.Name != "" {
		r.Name = b.Name
	}
}

// fillTitles fills a list's titles that lack the region's translation
// from the same list in the language alone. Failing that, they stay as
// they are.
func (c *Client) fillTitles(ctx context.Context, path string, q url.Values, titles []*rawTitle) {
	base := c.baseLanguage()
	if base == "" || !slices.ContainsFunc(titles, func(r *rawTitle) bool { return r.lacks(base) }) {
		return
	}
	var raw struct {
		Results []rawTitle `json:"results"`
	}
	if c.get(ctx, path, c.inBase(q), &raw) != nil {
		return
	}
	byID := map[int]rawTitle{}
	for _, b := range raw.Results {
		byID[b.ID] = b
	}
	for _, r := range titles {
		if b, ok := byID[r.ID]; ok && r.lacks(base) {
			r.fill(b)
		}
	}
}

// New returns a client for TMDB at baseURL (BaseURL, or a test server),
// with an operator's read access token.
func New(baseURL, token string) (*Client, error) {
	u, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || !ValidToken(token) {
		return nil, errors.New("tmdb: invalid address or token")
	}
	return &Client{base: u, token: token, slow: &gate{}, http: &http.Client{
		Timeout: 15 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}}, nil
}

// gate holds requests back until a time, as TMDB asks when it answers 429
// (too many requests).
type gate struct{ until atomic.Int64 } // Unix nanoseconds

// wait waits until the gate opens, or ctx ends.
func (g *gate) wait(ctx context.Context) error {
	d := time.Until(time.Unix(0, g.until.Load()))
	if d <= 0 {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

// close keeps the gate shut for d from now, or longer if it already is.
func (g *gate) close(d time.Duration) {
	until := time.Now().Add(d).UnixNano()
	for {
		old := g.until.Load()
		if old >= until || g.until.CompareAndSwap(old, until) {
			return
		}
	}
}

// retryAfter is how long TMDB asks to wait (Retry-After, in seconds): 10
// seconds when it doesn't say, 5 minutes at most.
func retryAfter(h http.Header) time.Duration {
	s, err := strconv.Atoi(strings.TrimSpace(h.Get("Retry-After")))
	if err != nil || s <= 0 {
		return 10 * time.Second
	}
	return min(time.Duration(s)*time.Second, 5*time.Minute)
}

// get reads path, with its query encoded, never pasted into the path
// (rule 2). When TMDB answers 429, every request waits as long as it asks,
// and this one is tried once more.
func (c *Client) get(ctx context.Context, path string, q url.Values, out any) error {
	err := c.getOnce(ctx, path, q, out)
	if errors.Is(err, errTooMany) {
		err = c.getOnce(ctx, path, q, out)
	}
	return err
}

// errTooMany: TMDB answered 429.
var errTooMany = errors.New("tmdb: too many requests")

func (c *Client) getOnce(ctx context.Context, path string, q url.Values, out any) error {
	if c.slow != nil {
		if err := c.slow.wait(ctx); err != nil {
			return err
		}
	}
	u := *c.base
	u.Path += path
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	res, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("tmdb: %s: %w", path, upstream.Plain(err))
	}
	defer res.Body.Close()
	body := io.LimitReader(res.Body, maxResponse)
	switch {
	case res.StatusCode == http.StatusUnauthorized:
		io.Copy(io.Discard, body)
		return ErrUnauthorized
	case res.StatusCode == http.StatusNotFound:
		io.Copy(io.Discard, body)
		return ErrNotFound
	case res.StatusCode == http.StatusTooManyRequests:
		io.Copy(io.Discard, body)
		if c.slow != nil {
			c.slow.close(retryAfter(res.Header))
		}
		return fmt.Errorf("%w (%s)", errTooMany, path)
	case res.StatusCode != http.StatusOK:
		io.Copy(io.Discard, body)
		return fmt.Errorf("tmdb: %s answered %d", path, res.StatusCode)
	}
	if err := json.NewDecoder(body).Decode(out); err != nil {
		return fmt.Errorf("tmdb: %s: decoding the answer: %w", path, err)
	}
	return nil
}

// rawTitle is a title as TMDB sends it, films and series alike.
type rawTitle struct {
	ID           int    `json:"id"`
	MediaType    string `json:"media_type"`
	Title        string `json:"title"` // films
	Name         string `json:"name"`  // series
	ReleaseDate  string `json:"release_date"`
	FirstAirDate string `json:"first_air_date"`
	Overview     string `json:"overview"`
	PosterPath   string `json:"poster_path"`
	BackdropPath string `json:"backdrop_path"`
	// OriginalTitle is a film's, OriginalName a series'.
	OriginalTitle    string `json:"original_title"`
	OriginalName     string `json:"original_name"`
	OriginalLanguage string `json:"original_language"`
}

func (r rawTitle) title(kind string) (Title, bool) {
	t, ok := MakeTitle(r.ID, kind, r.Title, r.ReleaseDate, r.Overview, r.PosterPath, r.BackdropPath, 0)
	original := r.OriginalTitle
	if kind == "series" {
		t, ok = MakeTitle(r.ID, kind, r.Name, r.FirstAirDate, r.Overview, r.PosterPath, r.BackdropPath, 0)
		original = r.OriginalName
	}
	t.OriginalName = textclean.Plain(original, 300)
	if isoLanguage.MatchString(r.OriginalLanguage) {
		t.Language = r.OriginalLanguage
	}
	return t, ok
}

// tv is TMDB's word for a kind in paths.
func tv(kind string) string { return map[string]string{"movie": "movie", "series": "tv"}[kind] }

func (c *Client) Discover(ctx context.Context, list, kind string, page int) (*Page, error) {
	k := tv(kind)
	if k == "" || page < 1 || page > 500 {
		return nil, errors.New("tmdb: invalid discovery query")
	}
	var path string
	q := url.Values{"page": {strconv.Itoa(page)}}
	switch list {
	case "trending":
		path = "/trending/" + k + "/week"
	case "popular":
		path = "/" + k + "/popular"
	case "upcoming":
		path = "/movie/upcoming"
		if kind == "series" {
			// Series that start today or later, the most awaited first
			// (TMDB's "on the air" lists series already running).
			path = "/discover/tv"
			q.Set("first_air_date.gte", time.Now().UTC().Format(time.DateOnly))
			q.Set("sort_by", "popularity.desc")
		}
	default:
		return nil, errors.New("tmdb: invalid discovery list")
	}
	return c.page(ctx, path, kind, q)
}

// Recommendations returns the first page of TMDB's titles like a film or
// a series, of the same kind.
func (c *Client) Recommendations(ctx context.Context, kind string, tmdbID int) (*Page, error) {
	k := tv(kind)
	if k == "" || !ValidID(tmdbID) {
		return nil, errors.New("tmdb: invalid recommendations query")
	}
	return c.page(ctx, "/"+k+"/"+strconv.Itoa(tmdbID)+"/recommendations", kind, url.Values{"page": {"1"}})
}

// ByGenre returns one page (from 1) of a kind's titles in one of TMDB's
// genres, the most popular first, among those enough people rated.
func (c *Client) ByGenre(ctx context.Context, kind string, genre, page int) (*Page, error) {
	k := tv(kind)
	if k == "" || genre < 1 || genre > 1_000_000 || page < 1 || page > 500 {
		return nil, errors.New("tmdb: invalid genre query")
	}
	return c.page(ctx, "/discover/"+k, kind, url.Values{"page": {strconv.Itoa(page)}, "with_genres": {strconv.Itoa(genre)},
		"sort_by": {"popularity.desc"}, "vote_count.gte": {"50"}})
}

// page reads one page of titles of a kind at path.
func (c *Client) page(ctx context.Context, path, kind string, q url.Values) (*Page, error) {
	k := tv(kind)
	var raw struct {
		Page       int        `json:"page"`
		TotalPages int        `json:"total_pages"`
		Results    []rawTitle `json:"results"`
	}
	q = c.withLanguage(q)
	if err := c.get(ctx, path, q, &raw); err != nil {
		return nil, err
	}
	var titles []*rawTitle
	for i := range raw.Results {
		titles = append(titles, &raw.Results[i])
	}
	c.fillTitles(ctx, path, q, titles)
	out := &Page{Page: raw.Page, TotalPages: min(raw.TotalPages, 500)}
	for _, r := range raw.Results {
		if r.MediaType != "" && r.MediaType != k {
			continue
		}
		if t, ok := r.title(kind); ok {
			out.Titles = append(out.Titles, t)
		}
	}
	return out, nil
}

// rawDetails is a title's page as TMDB sends it, with its credits and its
// certifications appended.
type rawDetails struct {
	rawTitle
	Genres []struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	} `json:"genres"`
	Credits struct {
		Cast []struct {
			ID          int    `json:"id"`
			Name        string `json:"name"`
			Character   string `json:"character"`
			ProfilePath string `json:"profile_path"`
		} `json:"cast"`
	} `json:"credits"`
	Seasons []struct {
		SeasonNumber int    `json:"season_number"`
		Name         string `json:"name"`
		EpisodeCount int    `json:"episode_count"`
		AirDate      string `json:"air_date"`
	} `json:"seasons"`
	ReleaseDates struct {
		Results []struct {
			Country      string `json:"iso_3166_1"`
			ReleaseDates []struct {
				Certification string `json:"certification"`
				Type          int    `json:"type"`
				ReleaseDate   string `json:"release_date"`
			} `json:"release_dates"`
		} `json:"results"`
	} `json:"release_dates"`
	ContentRatings struct {
		Results []struct {
			Country string `json:"iso_3166_1"`
			Rating  string `json:"rating"`
		} `json:"results"`
	} `json:"content_ratings"`
}

func (c *Client) details(ctx context.Context, kind string, tmdbID int) (*rawDetails, error) {
	k := tv(kind)
	if k == "" || !ValidID(tmdbID) {
		return nil, ErrNotFound
	}
	extra := "credits,release_dates"
	if kind == "series" {
		extra = "credits,content_ratings"
	}
	var raw rawDetails
	path := "/" + k + "/" + strconv.Itoa(tmdbID)
	if err := c.get(ctx, path, c.withLanguage(url.Values{"append_to_response": {extra}}), &raw); err != nil {
		return nil, err
	}
	if raw.ID != tmdbID {
		return nil, ErrNotFound
	}
	if base := c.baseLanguage(); base != "" && raw.lacks(base) {
		var b rawTitle
		if c.get(ctx, path, c.inBase(nil), &b) == nil && b.ID == tmdbID {
			raw.fill(b)
		}
	}
	return &raw, nil
}

func (c *Client) Details(ctx context.Context, kind string, tmdbID int) (*Details, error) {
	raw, err := c.details(ctx, kind, tmdbID)
	if err != nil {
		return nil, err
	}
	t, ok := raw.title(kind)
	if !ok {
		return nil, ErrNotFound
	}
	var genres []string
	for _, g := range raw.Genres {
		genres = append(genres, g.Name)
	}
	var cast []Person
	for _, p := range raw.Credits.Cast {
		cast = append(cast, Person{Name: p.Name, Character: p.Character, Profile: p.ProfilePath})
	}
	var seasons []Season
	for _, s := range raw.Seasons {
		seasons = append(seasons, Season{Number: s.SeasonNumber, Name: s.Name, EpisodeCount: s.EpisodeCount, AirDate: date(s.AirDate)})
	}
	return MakeDetails(t, genres, cast, seasons), nil
}

func (c *Client) Release(ctx context.Context, kind string, tmdbID int) (*Release, error) {
	k := tv(kind)
	if k == "" || !ValidID(tmdbID) {
		return nil, ErrNotFound
	}
	// Only what a release needs: not the cast and crew (a popular film's
	// are some hundred kilobytes), nor its names in a language.
	extra := "release_dates"
	if kind == "series" {
		extra = "content_ratings"
	}
	var raw rawDetails
	if err := c.get(ctx, "/"+k+"/"+strconv.Itoa(tmdbID), url.Values{"append_to_response": {extra}}, &raw); err != nil {
		return nil, err
	}
	if raw.ID != tmdbID {
		return nil, ErrNotFound
	}
	return raw.release(kind), nil
}

// release is a title's certifications and release dates, from its page.
func (raw *rawDetails) release(kind string) *Release {
	out := &Release{Ratings: Ratings{}}
	for _, r := range raw.ReleaseDates.Results {
		for _, d := range r.ReleaseDates {
			out.Ratings.Add(r.Country, d.Certification)
			out.AddDate(d.Type, d.ReleaseDate)
		}
	}
	for _, r := range raw.ContentRatings.Results {
		out.Ratings.Add(r.Country, r.Rating)
	}
	if kind == "series" {
		out.Released = date(raw.FirstAirDate)
		out.AtHome = out.Released
	}
	return out
}

func (c *Client) Certifications(ctx context.Context, kind string) (map[string][]Certification, error) {
	k := tv(kind)
	if k == "" {
		return nil, errors.New("tmdb: invalid kind")
	}
	var raw struct {
		Certifications map[string][]struct {
			Certification string `json:"certification"`
			Meaning       string `json:"meaning"`
			Order         int    `json:"order"`
		} `json:"certifications"`
	}
	if err := c.get(ctx, "/certification/"+k+"/list", nil, &raw); err != nil {
		return nil, err
	}
	scales := map[string][]Scale{}
	for cc, levels := range raw.Certifications {
		for _, l := range levels {
			scales[cc] = append(scales[cc], Scale{Certification: l.Certification, Meaning: l.Meaning, Order: l.Order})
		}
	}
	return MakeScales(scales), nil
}

// Artwork is a title's images, as TMDB paths (checked; "" when there's
// none): its poster and backdrop, and its logo (the title as art), in the
// client's language when TMDB has them; the logo only in it or without
// text.
type Artwork struct {
	Poster   string
	Backdrop string
	Logo     string
	// Textless is a poster without text (TMDB's without a language), ""
	// when there's none.
	Textless string
}

// pickTextless chooses, among posters (best first), the first without a
// language: without text.
func pickTextless(images []rawImage) string {
	for _, im := range images {
		if im.Language == "" && imagePath.MatchString(im.FilePath) {
			return im.FilePath
		}
	}
	return ""
}

// TextlessPoster returns a film's ("movie") or a series' ("series") poster
// without text, "" when TMDB has none: only its images without a language
// are asked for.
func (c *Client) TextlessPoster(ctx context.Context, kind string, tmdbID int) (string, error) {
	k := tv(kind)
	if k == "" || !ValidID(tmdbID) {
		return "", ErrNotFound
	}
	var raw struct {
		ID      int        `json:"id"`
		Posters []rawImage `json:"posters"`
	}
	if err := c.get(ctx, "/"+k+"/"+strconv.Itoa(tmdbID)+"/images", url.Values{"include_image_language": {"null"}}, &raw); err != nil {
		return "", err
	}
	if raw.ID != tmdbID {
		return "", ErrNotFound
	}
	return pickTextless(raw.Posters), nil
}

// rawImage is one of TMDB's images of a title.
type rawImage struct {
	FilePath string `json:"file_path"`
	Language string `json:"iso_639_1"`
	Region   string `json:"iso_3166_1"`
}

// pick chooses, among images (best first, as TMDB orders them), one in the
// language, the same region's first ("fr-CA": a Canadian one, else any
// French one), else one without text, else one in English.
func pick(images []rawImage, lang string) string {
	language, region, _ := strings.Cut(lang, "-")
	for _, want := range []rawImage{{Language: language, Region: region}, {Language: language}, {}, {Language: "en"}} {
		for _, im := range images {
			if im.Language == want.Language && (want.Region == "" || im.Region == want.Region) && imagePath.MatchString(im.FilePath) {
				return im.FilePath
			}
		}
	}
	return ""
}

// pickLogo chooses a title's logo, the title written as art: one in the
// language, the same region's first, as pick does, else one without text,
// else one in each of others in turn (the server's other languages, then
// the title's original one), else none (the title's name is then shown as
// text). "" is TMDB's default language, English.
func pickLogo(images []rawImage, lang string, others ...string) string {
	if lang == "" {
		lang = "en"
	}
	language, region, _ := strings.Cut(lang, "-")
	wants := []rawImage{{Language: language, Region: region}, {Language: language}, {}}
	for _, o := range others {
		if o, _, _ = strings.Cut(o, "-"); o != "" && o != language {
			wants = append(wants, rawImage{Language: o})
		}
	}
	for _, want := range wants {
		for _, im := range images {
			if im.Language == want.Language && (want.Region == "" || im.Region == want.Region) && imagePath.MatchString(im.FilePath) {
				return im.FilePath
			}
		}
	}
	return ""
}

// Logo returns a film's ("movie") or a series' ("series") logo in the
// client's language, chosen as pickLogo does; "" when there's none. Only
// its images are asked for, those in the language and those without text,
// so a logo can be checked without fetching the whole page again.
func (c *Client) Logo(ctx context.Context, kind string, tmdbID int) (string, error) {
	k := tv(kind)
	if k == "" || !ValidID(tmdbID) {
		return "", ErrNotFound
	}
	lang := c.language()
	if lang == "" {
		lang = "en"
	}
	var raw struct {
		ID    int        `json:"id"`
		Logos []rawImage `json:"logos"`
	}
	path := "/" + k + "/" + strconv.Itoa(tmdbID)
	if err := c.get(ctx, path+"/images", url.Values{"include_image_language": {c.imageLanguages(lang, false)}}, &raw); err != nil {
		return "", err
	}
	if raw.ID != tmdbID {
		return "", ErrNotFound
	}
	if logo := pickLogo(raw.Logos, lang, c.languages()...); logo != "" {
		return logo, nil
	}
	// None in the languages asked: one in its original language, if it's
	// another.
	var title struct {
		ID       int    `json:"id"`
		Original string `json:"original_language"`
	}
	// The title is known (its images were): not finding its language only
	// means no logo in it.
	if err := c.get(ctx, path, nil, &title); errors.Is(err, ErrNotFound) || (err == nil && title.ID != tmdbID) {
		return "", nil
	} else if err != nil {
		return "", err
	}
	logo, err := c.originalLogo(ctx, path, lang, title.Original)
	if errors.Is(err, ErrNotFound) {
		return "", nil
	}
	return logo, err
}

// isoLanguage is an ISO 639-1 code, as TMDB writes them.
var isoLanguage = regexp.MustCompile(`^[a-z]{2}$`)

// Check tells whether TMDB takes the client's key: nil, ErrUnauthorized, or
// another error when TMDB can't be reached.
func (c *Client) Check(ctx context.Context) error {
	var out struct {
		Success bool `json:"success"`
	}
	if err := c.get(ctx, "/authentication", nil, &out); err != nil {
		return err
	}
	if !out.Success {
		return ErrUnauthorized
	}
	return nil
}

// FilmCollection returns the collection a film belongs to (TMDB's series of
// films, such as a saga), 0 when it belongs to none: the film's page alone,
// nothing appended.
func (c *Client) FilmCollection(ctx context.Context, tmdbID int) (int, error) {
	if !ValidID(tmdbID) {
		return 0, ErrNotFound
	}
	var raw struct {
		ID         int `json:"id"`
		Collection *struct {
			ID int `json:"id"`
		} `json:"belongs_to_collection"`
	}
	if err := c.get(ctx, "/movie/"+strconv.Itoa(tmdbID), nil, &raw); err != nil {
		return 0, err
	}
	if raw.ID != tmdbID {
		return 0, ErrNotFound
	}
	if raw.Collection == nil || !ValidID(raw.Collection.ID) {
		return 0, nil
	}
	return raw.Collection.ID, nil
}

// Collection is one of TMDB's collections: a series of films, checked and
// in plain text, its films in release order (those without a date last).
type Collection struct {
	ID       int
	Name     string
	Overview string
	// Poster and Backdrop are TMDB image paths, checked like a title's.
	Poster, Backdrop string
	Parts            []Title
	// Released are the films' release dates (YYYY-MM-DD, "" when TMDB
	// doesn't say), by TMDB ID.
	Released map[int]string
}

// maxParts caps how many films a collection keeps.
const maxParts = 100

// Collection returns a collection with its films, in the client's language
// (what its translation lacks from the language alone).
func (c *Client) Collection(ctx context.Context, id int) (*Collection, error) {
	if !ValidID(id) {
		return nil, ErrNotFound
	}
	var raw struct {
		ID           int        `json:"id"`
		Name         string     `json:"name"`
		Overview     string     `json:"overview"`
		PosterPath   string     `json:"poster_path"`
		BackdropPath string     `json:"backdrop_path"`
		Parts        []rawTitle `json:"parts"`
	}
	path := "/collection/" + strconv.Itoa(id)
	if err := c.get(ctx, path, c.withLanguage(nil), &raw); err != nil {
		return nil, err
	}
	if raw.ID != id {
		return nil, ErrNotFound
	}
	// What the region's translation lacks, from the language alone: one
	// request for the collection and its films.
	if base := c.baseLanguage(); base != "" && (raw.Name == "" || raw.Overview == "" ||
		slices.ContainsFunc(raw.Parts, func(r rawTitle) bool { return r.lacks(base) })) {
		var b struct {
			ID       int        `json:"id"`
			Name     string     `json:"name"`
			Overview string     `json:"overview"`
			Parts    []rawTitle `json:"parts"`
		}
		if c.get(ctx, path, c.inBase(nil), &b) == nil && b.ID == id {
			raw.Name = cmp.Or(raw.Name, b.Name)
			raw.Overview = cmp.Or(raw.Overview, b.Overview)
			byID := map[int]rawTitle{}
			for _, p := range b.Parts {
				byID[p.ID] = p
			}
			for i := range raw.Parts {
				if p, ok := byID[raw.Parts[i].ID]; ok && raw.Parts[i].lacks(base) {
					raw.Parts[i].fill(p)
				}
			}
		}
	}
	out := &Collection{ID: id, Name: textclean.Plain(raw.Name, 300), Overview: textclean.Plain(raw.Overview, 2000),
		Released: map[int]string{}}
	if out.Name == "" {
		return nil, ErrNotFound
	}
	if imagePath.MatchString(raw.PosterPath) {
		out.Poster = raw.PosterPath
	}
	if imagePath.MatchString(raw.BackdropPath) {
		out.Backdrop = raw.BackdropPath
	}
	for _, p := range raw.Parts {
		t, ok := p.title("movie")
		if !ok || slices.ContainsFunc(out.Parts, func(x Title) bool { return x.TMDBID == t.TMDBID }) {
			continue
		}
		out.Parts = append(out.Parts, t)
		out.Released[t.TMDBID] = date(p.ReleaseDate)
		if len(out.Parts) == maxParts {
			break
		}
	}
	slices.SortStableFunc(out.Parts, func(a, b Title) int {
		da, db := out.Released[a.TMDBID], out.Released[b.TMDBID]
		switch {
		case da == db:
			return 0
		case da == "":
			return 1
		case db == "":
			return -1
		}
		return strings.Compare(da, db)
	})
	return out, nil
}

// languages are the server's languages, as Languages says; none when it
// doesn't.
func (c *Client) languages() []string {
	if c.Languages == nil {
		return nil
	}
	var out []string
	for _, l := range c.Languages() {
		if languageCode.MatchString(l) {
			out = append(out, l)
		}
	}
	return out
}

// imageLanguages is which images to ask for: those in the language (TMDB
// files images under the two-letter code), those without text, and, with
// logos, those in the server's other languages; English too for posters.
func (c *Client) imageLanguages(lang string, english bool) string {
	lang, _, _ = strings.Cut(lang, "-")
	if lang == "" {
		lang = "en"
	}
	want := []string{lang, "null"}
	if english {
		want = append(want, "en")
	}
	for _, l := range c.languages() {
		l, _, _ = strings.Cut(l, "-")
		if !slices.Contains(want, l) {
			want = append(want, l)
		}
	}
	return strings.Join(want, ",")
}

// originalLogo is a title's logo in its original language (ISO 639-1),
// when it's none of those already asked for; "" when there's none.
func (c *Client) originalLogo(ctx context.Context, path, lang, original string) (string, error) {
	if !isoLanguage.MatchString(original) || slices.Contains(strings.Split(c.imageLanguages(lang, true), ","), original) {
		return "", nil
	}
	var raw struct {
		Logos []rawImage `json:"logos"`
	}
	if err := c.get(ctx, path+"/images", url.Values{"include_image_language": {original}}, &raw); err != nil {
		return "", err
	}
	return pickLogo(raw.Logos, original), nil
}
