package tmdb

import (
	"context"
	"encoding/json"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/overfin/server/internal/textclean"
)

// EpisodeOrders is what TMDB says of a series' episodes, for an app that
// imports them: which episode a number means, and whether another order
// makes that number mean something else.
type EpisodeOrders struct {
	Seasons []OrderSeason `json:"seasons"`
}

// OrderSeason is one season in TMDB's own order. Rearranged: another order
// puts other episodes in its places.
type OrderSeason struct {
	Season     int            `json:"season"`
	Rearranged bool           `json:"rearranged"`
	Episodes   []OrderEpisode `json:"episodes"`
}

// OrderEpisode is one episode: Name is in English, Translated in the
// client's language, when that isn't English.
type OrderEpisode struct {
	Episode    int    `json:"episode"`
	AirDate    string `json:"airDate,omitempty"`
	Name       string `json:"name,omitempty"`
	Translated string `json:"translated,omitempty"`
}

// Episode group types that number seasons as releases do: original air,
// DVD, digital, production and TV. Absolute numbering and story arcs aren't
// seasons a release would follow.
var seasonGroupTypes = []int{1, 3, 4, 6, 7}

// maxOrderGroups bounds the groups read for one series: they're made by
// TMDB's users, and a series could have any number of them.
const maxOrderGroups = 20

// groupID is what an episode group's ID looks like.
var groupID = regexp.MustCompile(`^[0-9a-f]{1,64}$`)

// EpisodeOrders reads a series' seasons, in English and in the client's
// language, and its other orders. An order that can't be read is an error:
// no order is never said without having looked.
func (c *Client) EpisodeOrders(ctx context.Context, seriesID int) (*EpisodeOrders, error) {
	if !ValidID(seriesID) {
		return nil, ErrNotFound
	}
	path := "/tv/" + strconv.Itoa(seriesID)
	var show struct {
		Seasons []struct {
			Number int `json:"season_number"`
		} `json:"seasons"`
	}
	if err := c.get(ctx, path, nil, &show); err != nil {
		return nil, err
	}
	var numbers []int
	for _, s := range show.Seasons {
		if s.Number >= 0 && s.Number <= 1000 {
			numbers = append(numbers, s.Number)
		}
	}
	slices.Sort(numbers)

	english, err := c.rawSeasons(ctx, path, numbers, c.inEnglish(nil))
	if err != nil {
		return nil, err
	}
	translated := map[int]*rawSeason{}
	if c.inEnglish(nil) != nil {
		// The client's language is English already when inEnglish says nil:
		// then the first read was in it.
		if translated, err = c.rawSeasons(ctx, path, numbers, c.withLanguage(nil)); err != nil {
			return nil, err
		}
	}
	places := map[int][]int{}
	out := &EpisodeOrders{Seasons: []OrderSeason{}}
	for _, number := range numbers {
		raw := english[number]
		if raw == nil {
			continue
		}
		season := OrderSeason{Season: number, Episodes: []OrderEpisode{}}
		names := map[int]string{}
		if other := translated[number]; other != nil {
			for _, e := range other.Episodes {
				names[e.EpisodeNumber] = e.Name
			}
		}
		for _, e := range raw.Episodes {
			places[number] = append(places[number], e.ID)
			episode := OrderEpisode{Episode: e.EpisodeNumber, AirDate: date(e.AirDate), Name: e.Name}
			if name := names[e.EpisodeNumber]; name != e.Name {
				episode.Translated = name
			}
			season.Episodes = append(season.Episodes, episode)
		}
		out.Seasons = append(out.Seasons, season)
	}
	rearranged, err := c.rearrangedSeasons(ctx, path, places)
	if err != nil {
		return nil, err
	}
	for i := range out.Seasons {
		out.Seasons[i].Rearranged = rearranged[out.Seasons[i].Season]
	}
	return out, nil
}

// rawSeasons reads seasons as they are, a few per request, in the query's
// language.
func (c *Client) rawSeasons(ctx context.Context, path string, numbers []int, q url.Values) (map[int]*rawSeason, error) {
	out := map[int]*rawSeason{}
	for start := 0; start < len(numbers); start += seasonsPerRequest {
		batch := numbers[start:min(start+seasonsPerRequest, len(numbers))]
		parts := make([]string, 0, len(batch))
		for _, n := range batch {
			parts = append(parts, "season/"+strconv.Itoa(n))
		}
		query := url.Values{"append_to_response": {strings.Join(parts, ",")}}
		for k, v := range q {
			query[k] = v
		}
		var raw map[string]json.RawMessage
		if err := c.get(ctx, path, query, &raw); err != nil {
			return nil, err
		}
		for _, n := range batch {
			msg, ok := raw["season/"+strconv.Itoa(n)]
			if !ok {
				continue
			}
			var season rawSeason
			if err := json.Unmarshal(msg, &season); err != nil {
				return nil, err
			}
			out[n] = &season
		}
	}
	return out, nil
}

// EpisodeOrder is one of a series' other orders on TMDB, as an admin chooses
// among them.
type EpisodeOrder struct {
	ID   string
	Name string
	Type int // an episode group's type: 1 air, 3 DVD, 4 digital, 6 production, 7 TV
}

// ArrangedSeasons are a series' seasons and episodes as an order numbers
// them, by season number, each with TMDB's ID of its episodes. Season 0 is
// the order's specials, when it says which they are.
type ArrangedSeasons struct {
	Name    string
	Seasons map[int]*SeasonFull
}

// OtherOrders lists a series' orders that number seasons as releases do.
func (c *Client) OtherOrders(ctx context.Context, seriesID int) ([]EpisodeOrder, error) {
	if !ValidID(seriesID) {
		return nil, ErrNotFound
	}
	var list struct {
		Results []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
			Type int    `json:"type"`
		} `json:"results"`
	}
	if err := c.get(ctx, "/tv/"+strconv.Itoa(seriesID)+"/episode_groups", nil, &list); err != nil {
		return nil, err
	}
	out := []EpisodeOrder{}
	for _, r := range list.Results {
		if slices.Contains(seasonGroupTypes, r.Type) && groupID.MatchString(r.ID) && len(out) < maxOrderGroups {
			out = append(out, EpisodeOrder{ID: r.ID, Name: textclean.Plain(r.Name, 120), Type: r.Type})
		}
	}
	return out, nil
}

// rawGroupEpisode is an episode as an episode group lists it. Runtime and the
// rest are as a season's episodes.
type rawGroupEpisode struct {
	rawEpisode
	ShowID int `json:"show_id"`
	Order  int `json:"order"`
}

// specialsName is how a group's specials are named.
var specialsName = regexp.MustCompile(`(?i)\bsp[eé]cial`)

// ArrangedOrder reads an order of a series: another series' is ErrNotFound.
// A group's order says a season's number. Numbers start at 1, specials aside:
// a group whose first season, not its specials, has order 0 is shifted.
func (c *Client) ArrangedOrder(ctx context.Context, seriesID int, id string) (*ArrangedSeasons, error) {
	if !ValidID(seriesID) || !groupID.MatchString(id) {
		return nil, ErrNotFound
	}
	var raw struct {
		Name   string `json:"name"`
		Groups []struct {
			Name     string            `json:"name"`
			Order    int               `json:"order"`
			Episodes []rawGroupEpisode `json:"episodes"`
		} `json:"groups"`
	}
	if err := c.get(ctx, "/tv/episode_group/"+id, c.withLanguage(nil), &raw); err != nil {
		return nil, err
	}
	shift := 0
	for _, g := range raw.Groups {
		if g.Order == 0 && !specialsName.MatchString(g.Name) {
			shift = 1
		}
	}
	out := &ArrangedSeasons{Name: textclean.Plain(raw.Name, 120), Seasons: map[int]*SeasonFull{}}
	for _, g := range raw.Groups {
		number := g.Order + shift
		if specialsName.MatchString(g.Name) {
			number = 0
		}
		if number < 0 || number > 1000 || out.Seasons[number] != nil {
			continue
		}
		episodes := g.Episodes
		slices.SortStableFunc(episodes, func(a, b rawGroupEpisode) int { return a.Order - b.Order })
		season := rawSeason{Name: g.Name, SeasonNumber: &number}
		for i, e := range episodes {
			if e.ShowID != 0 && e.ShowID != seriesID {
				return nil, ErrNotFound
			}
			e.rawEpisode.EpisodeNumber = i + 1
			season.Episodes = append(season.Episodes, e.rawEpisode)
		}
		full, err := season.full(number)
		if err != nil {
			return nil, err
		}
		// A season's date is its first episode's.
		for _, e := range full.Episodes {
			if e.AirDate != "" && (full.AirDate == "" || e.AirDate < full.AirDate) {
				full.AirDate = e.AirDate
			}
		}
		out.Seasons[number] = full
	}
	if len(out.Seasons) == 0 {
		return nil, ErrNotFound
	}
	return out, nil
}

// rearrangedSeasons says which seasons another order fills differently,
// given the episode IDs in each season's places. An order that only stops
// earlier leaves the numbers it has their meaning, as one written before
// the season's last episodes aired does.
func (c *Client) rearrangedSeasons(ctx context.Context, path string, places map[int][]int) (map[int]bool, error) {
	var list struct {
		Results []struct {
			ID   string `json:"id"`
			Type int    `json:"type"`
		} `json:"results"`
	}
	if err := c.get(ctx, path+"/episode_groups", nil, &list); err != nil {
		return nil, err
	}
	out := map[int]bool{}
	read := 0
	for _, result := range list.Results {
		if !slices.Contains(seasonGroupTypes, result.Type) || !groupID.MatchString(result.ID) || read >= maxOrderGroups {
			continue
		}
		read++
		var group struct {
			Groups []struct {
				Order    int `json:"order"`
				Episodes []struct {
					ID    int `json:"id"`
					Order int `json:"order"`
				} `json:"episodes"`
			} `json:"groups"`
		}
		if err := c.get(ctx, "/tv/episode_group/"+result.ID, nil, &group); err != nil {
			return nil, err
		}
		for _, season := range group.Groups {
			mine := places[season.Order]
			episodes := season.Episodes
			slices.SortStableFunc(episodes, func(a, b struct {
				ID    int `json:"id"`
				Order int `json:"order"`
			}) int {
				return a.Order - b.Order
			})
			for i, e := range episodes {
				if i >= len(mine) || mine[i] != e.ID {
					out[season.Order] = true
					break
				}
			}
		}
	}
	return out, nil
}
