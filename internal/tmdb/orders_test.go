package tmdb

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestEpisodeOrders(t *testing.T) {
	groupsFail := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fr := r.URL.Query().Get("language") == "fr-CA"
		name := func(en, translated string) string {
			if fr {
				return translated
			}
			return en
		}
		switch r.URL.Path {
		case "/3/tv/615":
			if r.URL.Query().Get("append_to_response") == "" {
				io.WriteString(w, `{"seasons":[{"season_number":0},{"season_number":1},{"season_number":2},{"season_number":3}]}`)
				return
			}
			fmt.Fprintf(w, `{"season/0":{"episodes":[]},
				"season/1":{"episodes":[{"id":11,"episode_number":1,"air_date":"1999-03-28","name":%q},{"id":12,"episode_number":2,"air_date":"nonsense","name":"Same"}]},
				"season/2":{"episodes":[{"id":21,"episode_number":1,"name":"B1"},{"id":22,"episode_number":2,"name":"B2"}]},
				"season/3":{"episodes":[{"id":31,"episode_number":1,"name":"C1"},{"id":32,"episode_number":2,"name":"C2"}]}}`, name("Space Pilot", "Pilote"))
		case "/3/tv/615/episode_groups":
			if groupsFail {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			io.WriteString(w, `{"results":[{"id":"aa","type":6},{"id":"bb","type":5},{"id":"not/valid","type":3}]}`)
		case "/3/tv/episode_group/aa":
			// Season 1 stops earlier, so its numbers mean the same; season 2
			// swaps its episodes; a season TMDB's order hasn't is harmless to
			// the others.
			io.WriteString(w, `{"groups":[
				{"order":1,"episodes":[{"id":11,"order":0}]},
				{"order":2,"episodes":[{"id":21,"order":1},{"id":22,"order":0}]},
				{"order":9,"episodes":[{"id":31,"order":0}]}]}`)
		case "/3/tv/episode_group/bb":
			// A story arc: not an order a release follows.
			io.WriteString(w, `{"groups":[{"order":3,"episodes":[{"id":32,"order":0}]}]}`)
		default:
			t.Errorf("unexpected %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	c, err := New(srv.URL+"/3", testToken)
	if err != nil {
		t.Fatal(err)
	}
	c.Language = func() string { return "fr-CA" }
	orders, err := c.EpisodeOrders(context.Background(), 615)
	if err != nil {
		t.Fatal(err)
	}
	seasons := map[int]OrderSeason{}
	for _, s := range orders.Seasons {
		seasons[s.Season] = s
	}
	if len(seasons) != 4 || seasons[1].Rearranged || !seasons[2].Rearranged || seasons[3].Rearranged || seasons[0].Rearranged {
		t.Errorf("rearranged: %+v", orders.Seasons)
	}
	first := seasons[1].Episodes
	if first[0] != (OrderEpisode{Episode: 1, AirDate: "1999-03-28", Name: "Space Pilot", Translated: "Pilote"}) {
		t.Errorf("first episode: %+v", first[0])
	}
	if first[1] != (OrderEpisode{Episode: 2, Name: "Same"}) {
		t.Errorf("a name the same in both, and a bad date: %+v", first[1])
	}

	// An order that can't be read isn't "no other order".
	groupsFail = true
	if _, err := c.EpisodeOrders(context.Background(), 615); err == nil {
		t.Error("no error when the orders can't be read")
	}
}
