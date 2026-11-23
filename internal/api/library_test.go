package api_test

import (
	"bytes"
	"cmp"
	"context"
	"crypto/x509"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/msfp-io/msfp-go"
	"github.com/msfp-io/msfp-go/wire"
	"github.com/overfin/server/internal/api"
	"github.com/overfin/server/internal/catalog"
	"github.com/overfin/server/internal/config"
	"github.com/overfin/server/internal/delivery"
	"github.com/overfin/server/internal/emails"
	"github.com/overfin/server/internal/feature"
	"github.com/overfin/server/internal/identity"
	"github.com/overfin/server/internal/images"
	"github.com/overfin/server/internal/jellyfin"
	"github.com/overfin/server/internal/migration"
	"github.com/overfin/server/internal/oidc"
	"github.com/overfin/server/internal/pin"
	"github.com/overfin/server/internal/playback"
	"github.com/overfin/server/internal/plugin"
	"github.com/overfin/server/internal/previews"
	"github.com/overfin/server/internal/problem"
	"github.com/overfin/server/internal/proof"
	"github.com/overfin/server/internal/provision"
	"github.com/overfin/server/internal/seal"
	"github.com/overfin/server/internal/seerr"
	"github.com/overfin/server/internal/server"
	"github.com/overfin/server/internal/site"
	"github.com/overfin/server/internal/store"
	"github.com/overfin/server/internal/watchlist"
	web "github.com/overfin/web"
)

const (
	userJF       = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	token        = "profile-token"
	subject      = "test-subject"
	partnerJF    = "cccccccccccccccccccccccccccccccc"
	partnerToken = "partner-token"
)

// fakeJellyfin serves 7 movies and checks every call uses the profile's token.
type fakeJellyfin struct {
	mu        sync.Mutex
	badAuth   int
	lastQury  url.Values
	lastToken string         // the token of the last library call
	pgs       []byte         // the PGS track it extracts (subtitle 4)
	stretches []string       // subtitle stretches asked of the plugin: "track from to token"
	policy    map[string]any // the last policy set by the admin
	// partnerSeesAll gives the second profile the same library as the
	// first, instead of an empty one.
	partnerSeesAll bool
	users          map[string]string // created users: name -> ID (the first gets partnerJF)
}

func movie(i int) map[string]any {
	return map[string]any{"Id": fmt.Sprintf("%032x", i+1), "Name": fmt.Sprintf("Movie %d", i), "Type": "Movie"}
}

func (f *fakeJellyfin) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	authz := r.Header.Get("Authorization")
	switch {
	case r.URL.Path == "/Users/AuthenticateByName":
		var in struct{ Username string }
		json.NewDecoder(r.Body).Decode(&in)
		id := f.users[in.Username]
		if id == "" {
			id = partnerJF
		}
		json.NewEncoder(w).Encode(map[string]any{"AccessToken": partnerToken, "User": map[string]string{"Id": id}})
		return
	case strings.HasPrefix(r.URL.Path, "/Overfin/Subtitles/"+fmt.Sprintf("%032x", 1)+"/"+fmt.Sprintf("%032x", 1)+"/"):
		track := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		// Overfin's plugin answers Overfin's own key alone.
		if !strings.Contains(authz, `Token="admin"`) {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		f.stretches = append(f.stretches, track+" "+r.URL.Query().Get("from")+" "+r.URL.Query().Get("to"))
		switch track {
		case "3":
			fmt.Fprint(w, "WEBVTT\n\n00:00:01.000 --> 00:00:02.000\n<b>Hi</b>\n")
		case "4":
			w.Write(f.pgs)
		default:
			http.NotFound(w, r)
		}
		return
	case strings.Contains(authz, `Token="admin"`):
		switch r.Method + " " + r.URL.Path {
		case "POST /Users/New":
			var in struct{ Name string }
			json.NewDecoder(r.Body).Decode(&in)
			if f.users == nil {
				f.users = map[string]string{}
			}
			id := partnerJF
			if len(f.users) > 0 {
				id = fmt.Sprintf("%032x", 0xd0000+len(f.users))
			}
			f.users[in.Name] = id
			json.NewEncoder(w).Encode(map[string]string{"Id": id})
		default:
			switch {
			case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/Users/"):
				json.NewEncoder(w).Encode(map[string]any{"Policy": map[string]any{"IsAdministrator": false}})
			case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/Policy"):
				f.policy = nil
				json.NewDecoder(r.Body).Decode(&f.policy)
				w.WriteHeader(http.StatusNoContent)
			default:
				w.WriteHeader(http.StatusNoContent)
			}
		}
		return
	case strings.Contains(authz, `Token="`+partnerToken+`"`) && !f.partnerSeesAll:
		f.lastToken = partnerToken
		if r.URL.Query().Get("userId") != partnerJF && r.URL.Path != "/UserViews" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"Items": []any{}, "TotalRecordCount": 0})
		return
	case !strings.Contains(authz, `Token="`+token+`"`) && !(f.partnerSeesAll && strings.Contains(authz, `Token="`+partnerToken+`"`)):
		f.badAuth++
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	f.lastToken = token
	q := r.URL.Query()
	f.lastQury = q
	if q.Get("userId") != userJF && (!f.partnerSeesAll || q.Get("userId") != partnerJF) && !strings.HasPrefix(r.URL.Path, "/MediaSegments/") &&
		!strings.HasPrefix(strings.ToLower(r.URL.Path), "/videos/") {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	switch {
	case r.URL.Path == "/Items" && q.Has("ids"):
		var items []map[string]any
		for _, id := range strings.Split(q.Get("ids"), ",") {
			n, _ := strconv.ParseInt(id, 16, 64)
			if n > 1000 {
				continue // hidden from this profile
			}
			items = append(items, movie(int(n)-1))
		}
		json.NewEncoder(w).Encode(map[string]any{"Items": items})
	case r.URL.Path == "/Items" && q.Get("enableImages") == "false":
		var items []map[string]any
		for i := 0; i < 7; i++ {
			items = append(items, map[string]any{"Id": fmt.Sprintf("%032x", i+1)})
		}
		json.NewEncoder(w).Encode(map[string]any{"Items": items})
	case r.URL.Path == "/Items":
		start, _ := strconv.Atoi(q.Get("startIndex"))
		limit, _ := strconv.Atoi(q.Get("limit"))
		var items []map[string]any
		for i := start; i < 7 && i < start+limit; i++ {
			items = append(items, movie(i))
		}
		json.NewEncoder(w).Encode(map[string]any{"Items": items, "TotalRecordCount": 7})
	case r.URL.Path == "/Items/"+fmt.Sprintf("%032x", 3):
		json.NewEncoder(w).Encode(movie(2))
	case r.URL.Path == "/Items/"+fmt.Sprintf("%032x", 99):
		json.NewEncoder(w).Encode(map[string]any{"Id": fmt.Sprintf("%032x", 99), "Type": "Person",
			"Name": "Ada <b>Lovelace</b>", "Overview": "Wrote the first program.", "PremiereDate": "1815-12-10T00:00:00.0000000Z",
			"EndDate": "1852-11-27T00:00:00.0000000Z", "ProviderIds": map[string]string{"Imdb": "nm0000001", "Evil": "x"},
			"ImageTags": map[string]string{"Primary": fmt.Sprintf("%032x", 7)}})
	case r.URL.Path == "/Items/"+fmt.Sprintf("%032x", 1):
		json.NewEncoder(w).Encode(movie(0))
	case r.URL.Path == "/videos/"+fmt.Sprintf("%032x", 1)+"/hls1/main/0.mp4":
		w.Header().Set("Content-Type", "video/mp4")
		fmt.Fprint(w, "SEGMENT-ZERO")
	case r.URL.Path == "/MediaSegments/"+fmt.Sprintf("%032x", 1):
		json.NewEncoder(w).Encode(map[string]any{"Items": []map[string]any{
			{"Type": "Intro", "StartTicks": 50000000, "EndTicks": 900000000},
		}})
	case r.URL.Path == "/UserViews":
		// Two libraries: films (0xf1) and music (0xf2).
		json.NewEncoder(w).Encode(map[string]any{"Items": []map[string]any{
			{"Id": fmt.Sprintf("%032x", 0xf1), "Name": "Films", "Type": "CollectionFolder", "CollectionType": "movies"},
			{"Id": fmt.Sprintf("%032x", 0xf2), "Name": "Music", "Type": "CollectionFolder", "CollectionType": "music"},
		}})
	case strings.HasPrefix(r.URL.Path, "/Items/") && strings.HasSuffix(r.URL.Path, "/Ancestors"):
		// Items 6 and up are in the music library, the others in films.
		n, _ := strconv.ParseInt(strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/Items/"), "/Ancestors"), 16, 64)
		lib := 0xf1
		if n >= 6 {
			lib = 0xf2
		}
		json.NewEncoder(w).Encode([]map[string]any{
			{"Id": fmt.Sprintf("%032x", lib), "Type": "CollectionFolder"}, {"Id": fmt.Sprintf("%032x", 0xff), "Type": "AggregateFolder"},
		})
	case strings.HasPrefix(r.URL.Path, "/Items/"):
		w.WriteHeader(http.StatusNotFound) // not visible to this user
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func newServer(t *testing.T) (*httptest.Server, *fakeJellyfin) {
	return newServerLimited(t, 0)
}

func newServerLimited(t *testing.T, maxStreams int) (*httptest.Server, *fakeJellyfin) {
	t.Helper()
	return newServerWith(t, options{maxStreams: maxStreams})
}

// options changes how the test server is set up.
type options struct {
	maxStreams int
	// fileSize, for playedServer, is how large Heat's file is (10,000
	// bytes by default).
	fileSize int
	// maxTranscodes caps transcodes in total (none: no cap).
	maxTranscodes int
	// provider, when set, enables step-up; with login, also Overfin's own
	// login (identity mode "oidc"), re-checked on every request.
	provider *oidc.Provider
	login    bool
	// modules are extra modules to mount, as an operator's build would.
	modules []feature.Feature
	// settings are the modules' settings, by module name.
	settings map[string]map[string]string
	// admin, when set, receives the modules' control panel routes.
	admin *http.ServeMux
	// site serves overfin-web beside the API, as Overfin does by default.
	site bool
	// dbs, when set, receives the modules' own databases, by name.
	dbs *map[string]*sql.DB
	// template, when set, receives the ID of the test account's template
	// (3 profiles).
	template *string
	// seerr, when set, connects discovery to it; store receives the store.
	seerr *seerr.Client
	store **store.Store
	// dbPath, when set, receives the database file's path.
	dbPath *string
	// tmdb, when set, is TMDB for the pages fetched when opened.
	tmdb catalog.TitleSource
	// artworkDir, when set, is where TMDB's images are kept on disk.
	artworkDir string
	// wishes offers wishlists.
	wishes bool
	// adminGroup, when set, is the control panel's group, sent in the
	// Remote-Groups header.
	adminGroup string
	// managed signs people in with Overfin's own sign-in, sending codes
	// to mailbox (none when nil); signIn receives it.
	managed  bool
	mailbox  *mailbox
	signIn   **api.Managed
	freshFor time.Duration
	// signUp, when set, makes the identity provider's sign-up links.
	signUp func(ctx context.Context, previous string, uses int, until time.Time) (string, string, error)
	// playback, when set, receives the playback handler (the control
	// panel's live view uses it).
	playback *api.Playback
	// panel, when set, says where the control panel is (Sessions.Panel).
	panel func(*http.Request) string
	// log, when set, receives the server's log.
	log io.Writer
	// federation, when set, receives the server's federation, the MSFP
	// engine (its address the test server's, its name fedName or "B").
	federation **msfp.Engine
	fedName    string
	// events, when set, hears the engine's events too.
	events func(msfp.Event)
	// tickets, when set, receives the server's playbacks.
	tickets **playback.Tickets
	// wrap, when set, wraps the server's handler (to stand in for a
	// misbehaving server on some routes).
	wrap func(http.Handler) http.Handler
	// delivery, when set, carries out plays of the copy index's copies;
	// maxBitrateKbps is the operator's cap.
	delivery       *delivery.Executor
	maxBitrateKbps int
	// previews, when set, makes the seek-bar previews Overfin keeps.
	previews func(*store.Store) *previews.Keeper
	// jellyfin, when set, is a real test Jellyfin in place of the fake,
	// played as its user jellyfinUser with jellyfinToken.
	jellyfin, jellyfinUser, jellyfinToken string
	// adminKey is Overfin's key for that Jellyfin ("admin" by default).
	adminKey string
}

func newServerWith(t *testing.T, o options) (*httptest.Server, *fakeJellyfin) {
	maxStreams := o.maxStreams
	t.Helper()
	fake := &fakeJellyfin{}
	jfSrv := httptest.NewServer(fake)
	t.Cleanup(jfSrv.Close)
	jf, _ := jellyfin.New(jfSrv.URL, "test")
	jfUser, jfToken := userJF, token
	if o.jellyfin != "" {
		jf, _ = jellyfin.New(o.jellyfin, "test")
		jfUser, jfToken = o.jellyfinUser, o.jellyfinToken
	}

	box, _ := seal.New(bytes.Repeat([]byte{1}, 32))
	dbFile := filepath.Join(t.TempDir(), "t.db")
	st, err := store.Open(context.Background(), dbFile, box)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if o.dbPath != nil {
		*o.dbPath = dbFile
	}
	// The US scales, as TMDB has them (in part).
	st.SetRatingScales(context.Background(), "movie", map[string][]store.RatingLevel{"US": {
		{Certification: "NR", Position: 0}, {Certification: "G", Position: 1},
		{Certification: "PG", Position: 2}, {Certification: "PG-13", Position: 3}}})
	st.SetRatingScales(context.Background(), "series", map[string][]store.RatingLevel{"US": {
		{Certification: "TV-Y", Meaning: "All children", Position: 1}, {Certification: "TV-PG", Position: 3}}})
	acct, _, err := st.CreateAccount(context.Background(), subject, store.NewOwner{
		Name: "Main", JellyfinUserID: jfUser, JellyfinUsername: "of-test", JellyfinToken: jfToken,
	})
	if err != nil {
		t.Fatal(err)
	}
	// The test account's template: 3 profiles, and the stream limit asked
	// for (none means plenty).
	streams := maxStreams
	if streams == 0 {
		streams = 100
	}
	tmpl, err := st.CreateTemplate(context.Background(), "Test", streams, 3, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetAccountTemplate(context.Background(), acct.ID, tmpl.ID); err != nil {
		t.Fatal(err)
	}
	if o.template != nil {
		*o.template = tmpl.ID
	}
	// A second account, to check tickets don't work across accounts. Its
	// Jellyfin token differs, so the fake refuses its Jellyfin calls.
	if _, _, err := st.CreateAccount(context.Background(), "other-subject", store.NewOwner{
		Name: "Main", JellyfinUserID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", JellyfinUsername: "of-other", JellyfinToken: "other-token",
	}); err != nil {
		t.Fatal(err)
	}

	logger := slog.New(slog.NewTextHandler(cmp.Or[io.Writer](o.log, debugLog()), nil))
	prov := &provision.Provisioner{Store: st, Jellyfin: jf, AdminKey: cmp.Or(o.adminKey, "admin"), LogKey: []byte("k"), Logger: logger,
		AdminGroup: o.adminGroup}
	// Overfin's one Jellyfin account, which serves everyone: the fake's
	// user, with its token.
	if err := st.SetService(context.Background(), store.ServiceAccount{UserID: jfUser, Username: "overfin-service-test", Token: jfToken}); err != nil {
		t.Fatal(err)
	}
	service := &provision.Service{Provisioner: func() *provision.Provisioner { return prov }}
	sessions := &api.Sessions{Provisioner: prov, Store: st, Logger: logger, Lifetime: time.Hour, Panel: o.panel}
	extra := []plugin.Plugin{}
	ids := identity.NewForwardAuth([]netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}, "Remote-User", "Remote-Groups", "|")
	identify := ids.Middleware
	if o.provider != nil {
		auth := &api.Auth{Sessions: sessions, Store: st, Provisioner: prov, Provider: o.provider,
			FreshFor: 5 * time.Minute, Login: o.login, LoginLifetime: time.Hour, Logger: logger}
		sessions.StepUpURL, sessions.FreshFor = api.StepUpPath, auth.FreshFor
		extra = append(extra, auth)
		if o.login {
			sessions.SignIn = auth
			ids = identity.NewHeaderless([]netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")})
			identify = func(h http.Handler) http.Handler { return ids.Middleware(auth.Middleware(h)) }
		}
	}
	// checker2 is the pages' check against scripts; tests solve it with solve().
	checker2 := testProof
	var managed *api.Managed
	if o.managed {
		managed = &api.Managed{Sessions: sessions, Store: st, Provisioner: prov, CodeKey: []byte("code-key"),
			SiteURL: "https://media.example.org", LoginLifetime: time.Hour, FreshFor: cmp.Or(o.freshFor, 5*time.Minute), Logger: logger,
			Emails: &emails.Writer{Store: st, Logger: logger}, Proof: checker2}
		if box := o.mailbox; box != nil {
			managed.Mail = func() api.Mailer { return box }
		}
		prov.AdminGroup = api.ManagedAdminGroup
		sessions.StepUpURL, sessions.FreshFor, sessions.SignIn = api.StepUpPath, managed.FreshFor, managed
		extra = append(extra, managed)
		ids = identity.NewHeaderless([]netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")})
		identify = func(h http.Handler) http.Handler { return ids.Middleware(managed.Middleware(h)) }
		if o.signIn != nil {
			*o.signIn = managed
		}
	}
	lib := api.Library{Sessions: sessions, Provisioner: prov, Jellyfin: jf, CursorKey: []byte("cursor-key"), Service: service, Logger: logger,
		Delivery: o.delivery, MaxBitrateKbps: o.maxBitrateKbps, CopyKey: []byte("copy-key")}
	if o.tmdb != nil {
		src := o.tmdb
		lib.Titles = catalog.NewTitles(func() catalog.TitleSource { return src }, st, logger, time.Hour, 0)
	}
	if o.previews != nil {
		lib.Previews = o.previews(st)
	}
	lib.Images = images.New(jf, 1<<20)
	lib.Images.ArtworkDir = o.artworkDir
	if o.seerr != nil {
		extra = append(extra, &api.Discovery{Library: lib, Store: st})
	}
	if o.wishes {
		extra = append(extra, api.Wishes{Library: lib})
	}
	if o.store != nil {
		*o.store = st
	}
	pb := api.Playback{Library: lib, Tickets: playback.NewTickets(time.Hour), MaxTranscodes: o.maxTranscodes, Logger: logger,
		Delivery: o.delivery, MaxBitrateKbps: o.maxBitrateKbps}
	if o.tickets != nil {
		*o.tickets = pb.Tickets
	}
	// The server listens first: federation needs its address.
	srv := httptest.NewUnstartedServer(nil)
	var fed *msfp.Engine
	if o.federation != nil {
		var cur api.Playback
		host := api.PeerHost{Current: func() (api.Playback, bool) { return cur, true }}
		var err error
		fed, err = msfp.Open(context.Background(), msfp.Config{Address: "https://" + srv.Listener.Addr().String(),
			Store: st.MSFP(), Name: func() string { return cmp.Or(o.fedName, "B") }, Pins: []string{wire.Pin(testCertificate(t))},
			Parts: []string{"catalog", "playback", "media", "video"}, Catalog: store.MSFPCatalog{S: st},
			Playback: host, Media: host, AllowLoopback: true, Logger: logger, Clock: secondsClock{},
			Notifier: notifier(func(ev msfp.Event) {
				if e, ok := ev.(msfp.RemotePlaybackEnded); ok {
					cur.RemoteEnded(e.Playback)
				}
				if o.events != nil {
					o.events(ev)
				}
			}),
			NotFound: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { problem.Send(w, problem.NotFound, "") })})
		if err != nil {
			t.Fatal(err)
		}
		runCtx, stop := context.WithCancel(context.Background())
		t.Cleanup(stop)
		go fed.Run(runCtx)
		*o.federation = fed
		pb.Engine = func() *msfp.Engine { return fed }
		cur = pb
	}
	if o.playback != nil {
		*o.playback = pb
	}
	checker := &pin.Checker{Key: []byte("pin-key"), Store: st}
	// The watchlist and migration modules, mounted as the real program
	// mounts them, each with a database of its own; migration copies
	// histories from another server.
	dbs := map[string]*sql.DB{}
	moduleDB := func(m feature.Feature) *sql.DB {
		db, err := store.OpenModule(context.Background(), m.Info().Name,
			filepath.Join(t.TempDir(), m.Info().Name+".db"), m.Info().Migrations)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Close() })
		dbs[m.Info().Name] = db
		return db
	}
	all := append([]feature.Feature{watchlist.Feature{}, migration.Feature{}}, o.modules...)
	mounts := []api.Mount{
		{Module: watchlist.Feature{}, Library: lib, DB: moduleDB(watchlist.Feature{}), Logger: logger},
		{Module: migration.Feature{}, Library: lib, DB: moduleDB(migration.Feature{}), Logger: logger, Settings: map[string]string{"from": otherServer(t)}},
	}
	for _, m := range o.modules {
		mounts = append(mounts, api.Mount{Module: m, Library: lib, DB: moduleDB(m), Logger: logger,
			Settings: o.settings[m.Info().Name], Admin: o.admin, SignUp: o.signUp,
			SiteURL: "https://media.example.org"})
	}
	st.SetForgetter(testForgetter{modules: all, dbs: dbs})
	if o.dbs != nil {
		*o.dbs = dbs
	}
	for i := range mounts {
		mounts[i].Peers = mounts
		extra = append(extra, mounts[i])
	}
	sessions.Offers = api.Offers{Discovery: o.seerr != nil, Wishes: o.wishes}
	if sessions.SetupOffers, err = api.SetupOffers(mounts); err != nil {
		t.Fatal(err)
	}
	if managed != nil {
		managed.Mounts = mounts
	}
	plugins := append([]plugin.Plugin{lib, pb, api.Stream{Playback: pb},
		api.Account{Sessions: sessions, Provisioner: prov, Logger: logger, PINs: checker, Mounts: mounts},
		api.Profiles{Sessions: sessions, Provisioner: prov, Store: st, Service: service, LogoutURL: "/sign-out", Logger: logger,
			PINs: checker, EndPlaybacks: pb.EndWhere},
		api.Branding{Store: st},
		api.Info{Store: st},
		api.Pages{Proof: checker2},
		api.Invitations{Mounts: mounts, Managed: managed, Store: st, Logger: logger},
	}, extra...)
	var siteHandler http.Handler
	if o.site {
		if siteHandler, err = site.Embedded(web.Files, config.DefaultCSP, true, api.SiteGate(sessions.SignIn, prov)); err != nil {
			t.Fatal(err)
		}
	}
	if fed != nil {
		plugins = append(plugins, fedCalls{fed})
	}
	h, err := server.New(logger, identify, siteHandler, plugins...)
	if err != nil {
		t.Fatal(err)
	}
	// HTTPS, so the Secure session cookie behaves as in a browser.
	if o.wrap != nil {
		h = o.wrap(h)
	}
	srv.Config.Handler = h
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv, fake
}

var (
	clientsMu sync.Mutex
	clients   = map[string]*http.Client{}
)

// client returns one browser-like client per user and server: it trusts the
// test server's certificate and keeps cookies, like a browser would.
func client(srv *httptest.Server, who string) *http.Client {
	clientsMu.Lock()
	defer clientsMu.Unlock()
	key := srv.URL + "|" + who
	if c, ok := clients[key]; ok {
		return c
	}
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Transport: srv.Client().Transport, Jar: jar}
	clients[key] = c
	return c
}

func get(t *testing.T, srv *httptest.Server, path string, out any) int {
	t.Helper()
	req, _ := http.NewRequest("GET", srv.URL+path, nil)
	req.Header.Set("Remote-User", subject)
	res, err := client(srv, subject).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if out != nil {
		json.NewDecoder(res.Body).Decode(out)
	}
	return res.StatusCode
}

type page struct {
	Items      []struct{ ID, Kind, Name string }
	NextCursor string
	TotalCount int
	Seed       string
	Type       string // set on errors
}

// indexedServer is a test server whose index has 7 films, "Movie 0" to
// "Movie 6" (items 1 to 7), unknown to TMDB.
func indexedServer(t *testing.T) *httptest.Server {
	t.Helper()
	var st *store.Store
	srv, _ := newServerWith(t, options{store: &st})
	ctx := context.Background()
	var items []store.CatalogItem
	for i := 0; i < 7; i++ {
		name := fmt.Sprintf("Movie %d", i)
		items = append(items, store.CatalogItem{ID: fmt.Sprintf("%032x", i+1), Kind: "movie", Name: name, SortName: name})
	}
	if err := st.PutCatalog(ctx, items, 1); err != nil {
		t.Fatal(err)
	}
	st.RefreshTitleKeys(ctx)
	st.SetCatalogState(ctx, store.CatalogState{Pass: 1})
	return srv
}

func TestPagingWithSignedCursors(t *testing.T) {
	srv := indexedServer(t)

	var p1 page
	if code := get(t, srv, "/api/v1/titles?kind=movie&limit=3", &p1); code != 200 {
		t.Fatalf("status %d", code)
	}
	if len(p1.Items) != 3 || p1.TotalCount != 7 || p1.NextCursor == "" || p1.Items[0].Kind != "movie" {
		t.Fatalf("first page: %+v", p1)
	}

	var p2 page
	get(t, srv, "/api/v1/titles?kind=movie&limit=3&cursor="+url.QueryEscape(p1.NextCursor), &p2)
	if len(p2.Items) != 3 || p2.Items[0].Name != "Movie 3" {
		t.Errorf("second page: %+v", p2)
	}

	// The same cursor with other filters is refused.
	var bad page
	code := get(t, srv, "/api/v1/titles?kind=series&limit=3&cursor="+url.QueryEscape(p1.NextCursor), &bad)
	if code != http.StatusBadRequest || bad.Type != "invalid-cursor" {
		t.Errorf("reused cursor: %d %q", code, bad.Type)
	}
}

func TestInvalidQueries(t *testing.T) {
	srv, _ := newServer(t)
	for _, q := range []string{
		"?kind=folder",
		"?kind=episode",                    // seasons and episodes are on their series' page
		"?kind=movie&sort=name&seed=abc",   // a seed without random order
		"?kind=movie&sort=random&seed=a-b", // seeds are letters and digits
		"?kind=movie&limit=0",
		"?kind=movie&limit=500",
		"?kind=movie&library=../../x",
		"?kind=movie&genre=a|b",
		"?kind=movie&yearFrom=12",
		"?kind=movie&unknown=1",
		"?kind=movie&person=pers_1",
		"",
	} {
		var p page
		if code := get(t, srv, "/api/v1/titles"+q, &p); code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", q, code)
		}
	}
}

func TestTitleDetailAndUnknownTitles(t *testing.T) {
	srv := indexedServer(t)
	var it struct{ ID, Name, Kind string }
	if code := get(t, srv, "/api/v1/titles/title_item_"+fmt.Sprintf("%032x", 1), &it); code != 200 || it.Name != "Movie 0" || it.Kind != "movie" {
		t.Errorf("a file TMDB doesn't know: %d %+v", code, it)
	}
	if code := get(t, srv, "/api/v1/titles/title_item_"+fmt.Sprintf("%032x", 99), nil); code != http.StatusNotFound {
		t.Errorf("no such file: status %d", code)
	}
	if code := get(t, srv, "/api/v1/titles/not-an-id", nil); code != http.StatusNotFound {
		t.Errorf("malformed ID: status %d", code)
	}
}

func TestNoAccountMeansSetupRequired(t *testing.T) {
	srv, _ := newServer(t)
	req, _ := http.NewRequest("GET", srv.URL+"/api/v1/libraries", nil)
	req.Header.Set("Remote-User", "someone-new")
	res, err := client(srv, "someone-new").Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var p page
	json.NewDecoder(res.Body).Decode(&p)
	res.Body.Close()
	if p.Type != "account-setup-required" {
		t.Errorf("type = %q", p.Type)
	}
}

func names(p page) []string {
	var out []string
	for _, it := range p.Items {
		out = append(out, it.Name)
	}
	return out
}

func TestRandomOrderIsRepeatable(t *testing.T) {
	srv := indexedServer(t)

	var a, b, other page
	get(t, srv, "/api/v1/titles?kind=movie&sort=random&seed=abc&limit=7", &a)
	get(t, srv, "/api/v1/titles?kind=movie&sort=random&seed=abc&limit=7", &b)
	get(t, srv, "/api/v1/titles?kind=movie&sort=random&seed=xyz&limit=7", &other)
	if strings.Join(names(a), ",") != strings.Join(names(b), ",") {
		t.Errorf("same seed, different order: %v vs %v", names(a), names(b))
	}
	if strings.Join(names(a), ",") == strings.Join(names(other), ",") {
		t.Errorf("different seeds, same order: %v", names(a))
	}
	if len(a.Items) != 7 || a.Seed != "abc" {
		t.Errorf("page: %d items, seed %q", len(a.Items), a.Seed)
	}
}

func TestRandomPagesCoverEverythingOnce(t *testing.T) {
	srv := indexedServer(t)

	// No seed given: the server picks one, returns it, and the cursor keeps
	// using it on later pages.
	var p page
	get(t, srv, "/api/v1/titles?kind=movie&sort=random&limit=3", &p)
	if p.Seed == "" {
		t.Fatal("no seed returned")
	}
	firstSeed := p.Seed
	seen := map[string]bool{}
	for pages := 0; ; pages++ {
		if pages > 5 {
			t.Fatal("too many pages")
		}
		for _, n := range names(p) {
			if seen[n] {
				t.Fatalf("%s appeared twice", n)
			}
			seen[n] = true
		}
		if p.NextCursor == "" {
			break
		}
		next := p.NextCursor
		p = page{}
		get(t, srv, "/api/v1/titles?kind=movie&sort=random&limit=3&cursor="+url.QueryEscape(next), &p)
		if p.Seed != firstSeed {
			t.Fatalf("seed changed between pages: %q then %q", firstSeed, p.Seed)
		}
	}
	if len(seen) != 7 {
		t.Errorf("pages covered %d of 7 items", len(seen))
	}
}

func post(t *testing.T, srv *httptest.Server, path, body string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest("POST", srv.URL+path, strings.NewReader(body))
	req.Header.Set("Remote-User", subject)
	req.Header.Set("Content-Type", "application/json")
	res, err := client(srv, subject).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b)
}

func getAs(t *testing.T, srv *httptest.Server, who, path string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest("GET", srv.URL+path, nil)
	req.Header.Set("Remote-User", who)
	res, err := client(srv, who).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b)
}

// eventually waits for background reporting to reach the fake.
func eventually(t *testing.T, cond func() bool) bool {
	t.Helper()
	for i := 0; i < 100; i++ {
		if cond() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

// Every transcode taken: a playback that needs one is refused before the
// account's oldest stream stops, unless stopping it frees one (rule 49).
func TestTranscodeLimitStopsNothing(t *testing.T) {
	srv := playedServer(t, options{maxStreams: 2, maxTranscodes: 1}).srv
	file := func(device string) string { return startDelivery(t, srv, device, `"delivery":"file"`) }
	direct := file("Laptop")
	transcoded := startPlayback(t, srv, "Phone")
	// At the limit of 2, a transcode would replace the file's playback:
	// no transcode is free, so it's refused, and nothing stops.
	code, body := post(t, srv, "/api/v1/playback", `{"titleId":"title_movie_tmdb_949","versionId":"`+heatCopy+
		`","delivery":"transcode","transcode":{"videoCodec":"h264"},"device":"TV"}`)
	if code != http.StatusServiceUnavailable || !strings.Contains(body, "server-busy") {
		t.Errorf("a third: %d %s", code, body)
	}
	for _, id := range []string{direct, transcoded} {
		if code, _ := getAs(t, srv, subject, "/api/v1/playback/"+id); code != 200 {
			t.Errorf("%s after the refusal: %d", id, code)
		}
	}
	// When the oldest is the transcode, replacing it frees one.
	post(t, srv, "/api/v1/playback/"+direct+"/stop", `{"positionMs":0}`)
	file("TV")
	startPlayback(t, srv, "Tablet")
	if code, _ := getAs(t, srv, subject, "/api/v1/playback/"+transcoded); code != http.StatusGone {
		t.Errorf("the oldest, a transcode, replaced: %d", code)
	}
}

// The control panel's live view lists what plays, and stops it: the
// player learns an administrator stopped it, and its delivery stops.
func TestLiveViewStopsAPlayback(t *testing.T) {
	var pb api.Playback
	p := playedServer(t, options{maxStreams: 2, playback: &pb})
	srv := p.srv
	id := startPlayback(t, srv, "Bedroom laptop")

	live := pb.Tickets.Live(time.Now())
	if len(live) != 1 || live[0].Stream.ID != id || live[0].Stream.Device != "Bedroom laptop" ||
		live[0].Method != "transcode" {
		t.Fatalf("live: %+v", live)
	}
	if !pb.EndByAdmin(id) {
		t.Fatal("not stopped")
	}
	if pb.EndByAdmin(id) {
		t.Error("stopped twice")
	}
	code, body := getAs(t, srv, subject, "/api/v1/playback/"+id)
	if code != http.StatusGone || !strings.Contains(body, "playback-ended-by-admin") {
		t.Errorf("its player: %d %s", code, body)
	}
	if !eventually(t, func() bool { return p.maker.stops() == 1 }) {
		t.Error("its delivery was not stopped")
	}
	if live := pb.Tickets.Live(time.Now()); len(live) != 0 {
		t.Errorf("after: %+v", live)
	}
}

// Signing out ends the session's playbacks: its delivery stops, and the
// live view no longer lists it.
func TestLogoutEndsPlaybacks(t *testing.T) {
	var pb api.Playback
	p := playedServer(t, options{maxStreams: 2, playback: &pb})
	startPlayback(t, p.srv, "Bedroom laptop")
	if code, _ := post(t, p.srv, "/api/v1/logout", "{}"); code >= 300 {
		t.Fatalf("logout: %d", code)
	}
	if live := pb.Tickets.Live(time.Now()); len(live) != 0 {
		t.Errorf("after: %+v", live)
	}
	if !eventually(t, func() bool { return p.maker.stops() == 1 }) {
		t.Error("its delivery was not stopped")
	}
}

func TestPlaybackLifecycle(t *testing.T) {
	p := playedServer(t, options{maxStreams: 1})
	srv := p.srv

	first := startPlayback(t, srv, "Bedroom laptop")
	second := startPlayback(t, srv, "Living room TV")

	// The limit is 1: the first playback was taken over, and its device
	// learns by whom.
	code, body := getAs(t, srv, subject, "/api/v1/playback/"+first)
	var gone struct{ Type, TakenOverBy string }
	json.Unmarshal([]byte(body), &gone)
	if code != http.StatusGone || gone.Type != "playback-taken-over" || gone.TakenOverBy != "Living room TV" {
		t.Errorf("taken over: %d %+v", code, gone)
	}
	if !eventually(t, func() bool { return p.maker.stops() == 1 }) {
		t.Error("the taken-over delivery was not stopped")
	}

	// A reload restores the position reported.
	if code, _ := post(t, srv, "/api/v1/playback/"+second+"/progress", `{"positionMs":61000,"paused":true}`); code != http.StatusNoContent {
		t.Fatalf("progress: %d", code)
	}
	code, body = getAs(t, srv, subject, "/api/v1/playback/current")
	var cur struct {
		ID         string
		PositionMs int64
	}
	json.Unmarshal([]byte(body), &cur)
	if code != 200 || cur.ID != second || cur.PositionMs != 61000 {
		t.Errorf("current: %d %+v", code, cur)
	}

	// Stop ends the delivery; twice is fine.
	for i := 0; i < 2; i++ {
		if code, _ := post(t, srv, "/api/v1/playback/"+second+"/stop", `{"positionMs":90000}`); code != http.StatusNoContent {
			t.Errorf("stop %d: %d", i, code)
		}
	}
	if !eventually(t, func() bool { return p.maker.stops() == 2 }) {
		t.Error("the stopped delivery was not stopped")
	}
	code, body = getAs(t, srv, subject, "/api/v1/playback/"+second)
	json.Unmarshal([]byte(body), &gone)
	if code != http.StatusGone || gone.Type != "playback-stopped" {
		t.Errorf("after stop: %d %+v", code, gone)
	}
	if code, _ := getAs(t, srv, subject, "/api/v1/playback/current"); code != http.StatusNotFound {
		t.Errorf("current after stop: %d", code)
	}
	// Another account can neither see nor stop these.
	if code, _ := getAs(t, srv, "other-subject", "/api/v1/playback/"+second); code != http.StatusNotFound {
		t.Errorf("other account: %d", code)
	}
}

// testForgetter tells the modules of deleted profiles and accounts, as the
// real program does.
type testForgetter struct {
	modules []feature.Feature
	dbs     map[string]*sql.DB
}

func (f testForgetter) ForgetProfiles(ctx context.Context, ids []string) {
	for _, m := range f.modules {
		if mf, ok := m.(feature.Forgetter); ok {
			mf.ForgetProfiles(ctx, f.dbs[m.Info().Name], ids)
		}
	}
}

func (f testForgetter) ForgetAccount(ctx context.Context, id string) {
	for _, m := range f.modules {
		if mf, ok := m.(feature.Forgetter); ok {
			mf.ForgetAccount(ctx, f.dbs[m.Info().Name], id)
		}
	}
}

// otherServer is the Jellyfin server a site replaces, for migration: its
// user "old" (password "pw") watched one film, Fight Club; "boss" too, an
// administrator. Its address is returned.
func otherServer(t *testing.T) string {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/System/Info/Public":
			json.NewEncoder(w).Encode(map[string]any{"Version": "10.11.0", "Id": "0123456789abcdef0123456789abcdef", "StartupWizardCompleted": true})
		case "/Users/AuthenticateByName":
			var in struct{ Username, Pw string }
			json.NewDecoder(r.Body).Decode(&in)
			ids := map[string]string{"old": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "old2": "cccccccccccccccccccccccccccccccc"}
			if ids[in.Username] == "" || in.Pw != "pw" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"AccessToken": "old-token", "User": map[string]string{"Id": ids[in.Username]}})
		case "/Items":
			if r.URL.Query().Get("isPlayed") != "true" {
				json.NewEncoder(w).Encode(map[string]any{"Items": []any{}})
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"Items": []any{map[string]any{"Id": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "Type": "Movie",
				"Name": "Fight Club", "ProviderIds": map[string]string{"Tmdb": "550"}, "UserData": map[string]any{"Played": true, "PlayCount": 1}}}})
		case "/Sessions/Logout":
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// testProof is the pages' check against scripts in tests: solve answers
// it.
var testProof = &proof.Checker{Key: []byte("proof-key")}

// solve gets a check from the pages and answers it, as a page's script
// does.
func solve(t *testing.T, srv *httptest.Server) map[string]string {
	t.Helper()
	res, err := srv.Client().Get(srv.URL + "/api/v1/pages/challenge")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var c struct{ Challenge string }
	json.NewDecoder(res.Body).Decode(&c)
	for n := 0; ; n++ {
		if a := strconv.Itoa(n); proof.Solves(c.Challenge, a) {
			return map[string]string{"challenge": c.Challenge, "answer": a}
		}
	}
}

// pgsTrack is a well-formed PGS track: one 1 by 1 picture shown, then
// hidden.
func pgsTrack() []byte {
	seg := func(typ byte, body ...byte) []byte {
		return append([]byte{'P', 'G', 0, 0, 0, 0, 0, 0, 0, 0, typ, 0, byte(len(body))}, body...)
	}
	var t []byte
	t = append(t, seg(0x16, 7, 128, 4, 56, 16, 0, 0, 0x80, 0, 0, 1, 0, 1, 0, 0, 1, 0, 2, 0)...)
	t = append(t, seg(0x14, 0, 0, 1, 235, 128, 128, 255)...)
	t = append(t, seg(0x15, 0, 1, 0, 0xC0, 0, 0, 0, 0, 1, 0, 1, 1, 0, 0)...)
	t = append(t, seg(0x80)...)
	return append(t, seg(0x16, 7, 128, 4, 56, 16, 0, 1, 0, 0, 0, 0)...)
}

// testCertificate is the certificate every httptest TLS server serves.
func testCertificate(t *testing.T) *x509.Certificate {
	t.Helper()
	srv := httptest.NewTLSServer(http.NotFoundHandler())
	defer srv.Close()
	return srv.Certificate()
}

// notifier is a func as an msfp.Notifier.
type notifier func(msfp.Event)

func (n notifier) Notify(ev msfp.Event) { n(ev) }

// fedCalls mounts the engine's federation calls, as Overfin does.
type fedCalls struct{ engine *msfp.Engine }

func (fedCalls) Name() string { return "federation" }

func (f fedCalls) Register(r *plugin.Registrar) error {
	for _, call := range []string{"pair", "hello", "rotate", "revoke", "libraries", "titles", "play", "progress", "stop", "previews"} {
		if err := r.HandlePublic("POST /api/v1/federation/"+call, f.engine.FederationHandler()); err != nil {
			return err
		}
	}
	return nil
}

func debugLog() io.Writer {
	if os.Getenv("OVERFIN_TEST_LOG") != "" {
		return os.Stderr
	}
	return io.Discard
}

// secondsClock is the system's time cut to the second: an engine started
// at it hears requests dated the second it started (MSFP core section
// 6.4, step 4), which tests make at once.
type secondsClock struct{}

func (secondsClock) Now() time.Time { return time.Now().Truncate(time.Second) }
