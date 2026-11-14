package tmdb

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
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
			io.WriteString(w, `{"results":[{"id":"aa","type":6},{"id":"bb","type":5},{"id":"not/valid","type":3},{"id":"cc","type":3}]}`)
		case "/3/tv/episode_group/aa":
			// Season 1 stops earlier, so its numbers mean the same; season 2
			// swaps its episodes; a season TMDB's order hasn't is harmless to
			// the others.
			io.WriteString(w, `{"groups":[
				{"order":1,"episodes":[{"id":11,"order":0}]},
				{"order":2,"episodes":[{"id":21,"order":1},{"id":22,"order":0}]},
				{"order":9,"episodes":[{"id":31,"order":0}]}]}`)
		case "/3/tv/episode_group/cc":
			// Counted from 0, specials named: its "order 0" is season 1, the
			// same as TMDB's, and its season 2 holds three episodes.
			io.WriteString(w, `{"groups":[
				{"order":0,"name":"Season 1","episodes":[{"id":11,"order":0},{"id":12,"order":1}]},
				{"order":1,"name":"Season 2","episodes":[{"id":22,"order":0},{"id":21,"order":1},{"id":31,"order":2}]},
				{"order":5,"name":"Specials","episodes":[]}]}`)
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
	if !slices.Equal(seasons[2].OtherSizes, []int{2, 3}) || len(seasons[1].OtherSizes) != 0 || seasons[1].OtherSizes == nil {
		t.Errorf("other sizes: %v, %v", seasons[2].OtherSizes, seasons[1].OtherSizes)
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

func TestArrangedOrder(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/3/tv/615/episode_groups":
			io.WriteString(w, `{"results":[{"id":"aa","name":"Production","type":6},{"id":"bb","name":"Arcs","type":5},{"id":"not/valid","type":3}]}`)
		case "/3/tv/episode_group/aa":
			io.WriteString(w, `{"name":"Production","groups":[
				{"name":"Specials","order":0,"episodes":[{"id":91,"order":0,"show_id":615,"name":"Bonus"}]},
				{"name":"Season 1","order":1,"episodes":[
					{"id":12,"order":1,"show_id":615,"name":"Second","air_date":"1999-04-02","season_number":4,"episode_number":9,"runtime":22,"still_path":"/still1234.jpg"},
					{"id":11,"order":0,"show_id":615,"name":"First","air_date":"1999-03-28","season_number":1,"episode_number":3}]}]}`)
		case "/3/tv/episode_group/cc":
			io.WriteString(w, `{"groups":[{"name":"Volume 1","order":0,"episodes":[{"id":11,"order":0,"show_id":615}]},
				{"name":"Volume 2","order":1,"episodes":[{"id":12,"order":0,"show_id":615}]}]}`)
		case "/3/tv/episode_group/dd":
			io.WriteString(w, `{"groups":[{"name":"S","order":1,"episodes":[{"id":5,"order":0,"show_id":999}]}]}`)
		default:
			t.Errorf("unexpected %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	c, err := New(srv.URL+"/3", testToken)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	others, err := c.OtherOrders(ctx, 615)
	if err != nil || len(others) != 1 || others[0].ID != "aa" || others[0].Name != "Production" || others[0].Type != 6 {
		t.Fatalf("%+v %v", others, err)
	}
	order, err := c.ArrangedOrder(ctx, 615, "aa")
	if err != nil {
		t.Fatal(err)
	}
	one := order.Seasons[1]
	if one == nil || len(one.Episodes) != 2 || one.AirDate != "1999-03-28" {
		t.Fatalf("%+v", one)
	}
	first, second := one.Episodes[0], one.Episodes[1]
	if first.ID != 11 || first.Number != 1 || second.ID != 12 || second.Number != 2 || second.Runtime != 22 || second.Still != "/still1234.jpg" {
		t.Errorf("%+v %+v", first, second)
	}
	if sp := order.Seasons[0]; sp == nil || sp.Episodes[0].ID != 91 {
		t.Errorf("specials: %+v", sp)
	}
	// Orders from 0 without specials start at season 1.
	shifted, err := c.ArrangedOrder(ctx, 615, "cc")
	if err != nil || shifted.Seasons[0] != nil || shifted.Seasons[1] == nil || shifted.Seasons[2] == nil {
		t.Errorf("%+v %v", shifted, err)
	}
	if _, err := c.ArrangedOrder(ctx, 615, "dd"); !errors.Is(err, ErrNotFound) {
		t.Errorf("another series' order: %v", err)
	}
	if _, err := c.ArrangedOrder(ctx, 615, "not/valid"); !errors.Is(err, ErrNotFound) {
		t.Errorf("a bad ID: %v", err)
	}
}
