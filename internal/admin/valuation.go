package admin

import (
	"context"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/overfin/server/internal/imdb"
	"github.com/overfin/server/internal/store"
	"github.com/overfin/server/internal/textclean"
	"github.com/overfin/server/internal/valuation"
)

// measuredFor is how long a measurement of the titles serves the
// Valuation view before it's made again: the sliders move often, what
// people do changes slowly.
const measuredFor = time.Minute

// measurement keeps the last measurement of the titles.
type measurement struct {
	mu sync.Mutex
	at time.Time
	m  *valuation.Measured
	f  *store.ValuationFacts
}

// measured is every title ranked on every criterion, at most measuredFor
// old.
func (p *Panel) measured(ctx context.Context) (*valuation.Measured, error) {
	m, _, err := p.measuredFacts(ctx)
	return m, err
}

// measuredFacts is measured, with the facts it was measured from.
func (p *Panel) measuredFacts(ctx context.Context) (*valuation.Measured, *store.ValuationFacts, error) {
	p.valued.mu.Lock()
	defer p.valued.mu.Unlock()
	now := p.clock()
	if p.valued.m != nil && now.Sub(p.valued.at) < measuredFor {
		return p.valued.m, p.valued.f, nil
	}
	var interests []store.ProfileTitle
	if p.Interests != nil {
		var err error
		if interests, err = p.Interests(ctx); err != nil {
			return nil, nil, err
		}
	}
	f, err := p.Store.ValuationFacts(ctx, now, interests, p.Features.Features().Wishes.KeptDays)
	if err != nil {
		return nil, nil, err
	}
	if p.IMDb != nil {
		withIMDb(f, p.IMDb.Data())
	}
	p.valued.m, p.valued.f, p.valued.at = valuation.Measure(f, now), f, now
	return p.valued.m, f, nil
}

// withIMDb gives the valued titles IMDb's ratings, from d (none when nil):
// a film's own, a season's from its episodes, or its series' when IMDb
// numbers the seasons otherwise.
func withIMDb(f *store.ValuationFacts, d *imdb.Data) {
	if d == nil {
		return
	}
	for _, t := range f.Titles {
		if t.IMDb == "" {
			continue
		}
		if r, _ := store.ParseTitleKey(t.Key); r.Kind == "season" {
			if s, ok := d.Season(t.IMDb, t.Season); ok {
				t.IMDbRating, t.IMDbVotes = s.Rating, s.Votes
				continue
			}
		}
		if it, ok := d.Title(t.IMDb); ok {
			t.IMDbRating, t.IMDbVotes = it.Rating, it.Votes
		}
	}
}

type criterionJSON struct {
	Name    string `json:"name"`
	Weight  int    `json:"weight"`
	Default int    `json:"default"`
}

type valuationJSON struct {
	Criteria []criterionJSON `json:"criteria"`
	Max      int             `json:"max"`
	// Accounts is how many accounts count (used in the last six months);
	// Titles, how many films and seasons are valued.
	Accounts int `json:"accounts"`
	Titles   int `json:"titles"`
	// AskTastes: accounts are asked what they like (Tastes then mixes
	// what they said with what they watch; otherwise only the latter).
	AskTastes bool `json:"askTastes"`
	// IMDb is IMDb's data, when this Overfin can have it.
	IMDb *imdbJSON `json:"imdb,omitempty"`
}

// imdbJSON is where IMDb's data is.
type imdbJSON struct {
	On bool `json:"on"`
	// Titles and Built are the file in use's: how many titles, and when
	// it was made (none yet: 0 and absent).
	Titles int    `json:"titles"`
	Built  string `json:"built,omitempty"`
	// Working: being downloaded now. Problem: why the last try failed.
	Working bool   `json:"working"`
	Problem string `json:"problem,omitempty"`
}

// valuationSettings (GET /admin/api/valuation) answers the weights, the
// defaults, and what the valuation counts.
func (p *Panel) valuationSettings(w http.ResponseWriter, r *http.Request, _ *session) {
	p.writeValuation(w, r)
}

func (p *Panel) writeValuation(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	saved, err := p.Store.ValuationWeights(ctx)
	if err != nil {
		p.fail(w, "reading the valuation's weights", err)
		return
	}
	m, err := p.measured(ctx)
	if err != nil {
		p.fail(w, "measuring the titles", err)
		return
	}
	asked, err := p.Store.TastesAsked(ctx)
	if err != nil {
		p.fail(w, "reading whether tastes are asked", err)
		return
	}
	weights, defaults := valuation.Weights(saved), valuation.Defaults()
	out := valuationJSON{Max: valuation.MaxWeight, Accounts: m.Accounts, Titles: len(m.Titles), AskTastes: asked}
	if p.IMDb != nil {
		on, err := p.Store.IMDbOn(ctx)
		if err != nil {
			p.fail(w, "reading whether IMDb's data is on", err)
			return
		}
		st := p.IMDb.Status()
		out.IMDb = &imdbJSON{On: on, Titles: st.Titles, Working: st.Working, Problem: st.Problem}
		if !st.Built.IsZero() {
			out.IMDb.Built = st.Built.UTC().Format(time.RFC3339)
		}
	}
	for _, c := range valuation.Criteria {
		out.Criteria = append(out.Criteria, criterionJSON{Name: c, Weight: weights[c], Default: defaults[c]})
	}
	writeJSON(w, http.StatusOK, out)
}

// weightsOf checks weights sent by the panel: every criterion known, each
// from 0 to the most; on failure it has answered.
func weightsOf(w http.ResponseWriter, sent map[string]int) (map[string]int, bool) {
	for c, v := range sent {
		if !slices.Contains(valuation.Criteria, c) || v < 0 || v > valuation.MaxWeight {
			invalid(w, "weights go from 0 to "+strconv.Itoa(valuation.MaxWeight)+", for the known criteria")
			return nil, false
		}
	}
	return valuation.Weights(sent), true
}

// setValuation (PUT /admin/api/valuation) keeps new weights, or with
// reset, goes back to the defaults; with askTastes or imdb alone, it only
// switches asking accounts what they like, or IMDb's data.
func (p *Panel) setValuation(w http.ResponseWriter, r *http.Request, s *session) {
	var req struct {
		Weights   map[string]int `json:"weights"`
		Reset     bool           `json:"reset"`
		AskTastes *bool          `json:"askTastes"`
		IMDb      *bool          `json:"imdb"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.IMDb != nil {
		if p.IMDb == nil {
			invalid(w, "IMDb's data isn't available here")
			return
		}
		if err := p.Store.SetIMDbOn(r.Context(), *req.IMDb); err != nil {
			p.fail(w, "switching IMDb's data", err)
			return
		}
		p.IMDb.Wake()
		p.Logger.Info("IMDb's data switched in the control panel", "on", *req.IMDb, "by", s.logID)
		if req.Weights == nil && !req.Reset && req.AskTastes == nil {
			p.writeValuation(w, r)
			return
		}
	}
	if req.AskTastes != nil {
		if err := p.Store.SetTastesAsked(r.Context(), *req.AskTastes); err != nil {
			p.fail(w, "switching asking tastes", err)
			return
		}
		p.valued.mu.Lock()
		p.valued.m = nil // measured again, with or without what accounts said
		p.valued.mu.Unlock()
		p.Logger.Info("asking accounts what they like switched in the control panel", "on", *req.AskTastes, "by", s.logID)
		if req.Weights == nil && !req.Reset {
			p.writeValuation(w, r)
			return
		}
	}
	var weights map[string]int
	if !req.Reset {
		var ok bool
		if weights, ok = weightsOf(w, req.Weights); !ok {
			return
		}
	}
	if err := p.Store.SetValuationWeights(r.Context(), weights); err != nil {
		p.fail(w, "saving the valuation's weights", err)
		return
	}
	p.Logger.Info("the valuation's weights changed in the control panel", "reset", req.Reset, "by", s.logID)
	p.writeValuation(w, r)
}

type valuedJSON struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	Season    int     `json:"season,omitempty"`
	InLibrary bool    `json:"inLibrary"`
	Score     float64 `json:"score"`
	// Parts are the title's ranks on the criteria that don't come from
	// what people do (TMDB's, the libraries'); those that do are only in
	// the score, so the panel never shows anyone's activity per title.
	Parts map[string]float64 `json:"parts"`
}

// previewSize is how many titles each end of the preview shows, and the
// most a search finds.
const previewSize = 10

// valuationPreview (POST /admin/api/valuation/preview) scores the titles
// under weights not saved yet: the most and the least valuable among those
// the libraries have, and with a query, the titles whose name has it.
func (p *Panel) valuationPreview(w http.ResponseWriter, r *http.Request, _ *session) {
	var req struct {
		Weights map[string]int `json:"weights"`
		Query   string         `json:"query"`
	}
	if !decode(w, r, &req) {
		return
	}
	weights, ok := weightsOf(w, req.Weights)
	if !ok {
		return
	}
	m, err := p.measured(r.Context())
	if err != nil {
		p.fail(w, "measuring the titles", err)
		return
	}
	scored := m.Scored(weights)
	var library []valuation.Value
	for _, v := range scored {
		if v.InLibrary {
			library = append(library, v)
		}
	}
	out := map[string][]valuedJSON{"most": {}, "least": {}, "found": {}}
	for i := 0; i < len(library) && i < previewSize; i++ {
		out["most"] = append(out["most"], valuedOf(library[i]))
	}
	for i := len(library) - 1; i >= 0 && len(out["least"]) < previewSize; i-- {
		out["least"] = append(out["least"], valuedOf(library[i]))
	}
	if q := store.Fold(textclean.Plain(req.Query, 100)); q != "" {
		for _, v := range scored {
			if strings.Contains(store.Fold(v.Name), q) {
				out["found"] = append(out["found"], valuedOf(v))
				if len(out["found"]) == previewSize {
					break
				}
			}
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func valuedOf(v valuation.Value) valuedJSON {
	parts := map[string]float64{}
	for c, part := range v.Parts {
		if !valuation.FromPeople(c) {
			parts[c] = part
		}
	}
	return valuedJSON{ID: "title_" + strings.ReplaceAll(v.Key, "/", "_"), Name: v.Name, Season: v.Season,
		InLibrary: v.InLibrary, Score: v.Score, Parts: parts}
}
