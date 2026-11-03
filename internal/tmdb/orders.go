package tmdb

import (
	"context"
	"encoding/json"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
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
