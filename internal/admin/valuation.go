package admin

import (
	"context"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

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
}

// measured is every title ranked on every criterion, at most measuredFor
// old.
func (p *Panel) measured(ctx context.Context) (*valuation.Measured, error) {
	p.valued.mu.Lock()
	defer p.valued.mu.Unlock()
	now := p.clock()
	if p.valued.m != nil && now.Sub(p.valued.at) < measuredFor {
		return p.valued.m, nil
	}
	var interests []store.ProfileTitle
	if p.Interests != nil {
		var err error
		if interests, err = p.Interests(ctx); err != nil {
			return nil, err
		}
	}
	f, err := p.Store.ValuationFacts(ctx, now, interests)
	if err != nil {
		return nil, err
	}
	p.valued.m, p.valued.at = valuation.Measure(f, now), now
	return p.valued.m, nil
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
	weights, defaults := valuation.Weights(saved), valuation.Defaults()
	out := valuationJSON{Max: valuation.MaxWeight, Accounts: m.Accounts, Titles: len(m.Titles)}
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
// reset, goes back to the defaults.
func (p *Panel) setValuation(w http.ResponseWriter, r *http.Request, s *session) {
	var req struct {
		Weights map[string]int `json:"weights"`
		Reset   bool           `json:"reset"`
	}
	if !decode(w, r, &req) {
		return
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
	p.Logger.Info("the valuation's weights changed in the control panel", "reset", req.Reset, "by", s.name)
	p.writeValuation(w, r)
}

type valuedJSON struct {
	ID        string             `json:"id"`
	Name      string             `json:"name"`
	Season    int                `json:"season,omitempty"`
	InLibrary bool               `json:"inLibrary"`
	Score     float64            `json:"score"`
	Parts     map[string]float64 `json:"parts"`
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
	return valuedJSON{ID: "title_" + strings.ReplaceAll(v.Key, "/", "_"), Name: v.Name, Season: v.Season,
		InLibrary: v.InLibrary, Score: v.Score, Parts: v.Parts}
}
