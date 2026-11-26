package jellyfin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// deliveryPlugin is a fake of Overfin's plugin's delivery routes: it
// answers with status and body, and records each request.
type deliveryPlugin struct {
	mu       sync.Mutex
	status   int
	body     string
	requests []string // "METHOD path"
	auth     []string
	sent     []map[string]any
}

func (p *deliveryPlugin) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.requests = append(p.requests, r.Method+" "+r.URL.Path)
	p.auth = append(p.auth, r.Header.Get("Authorization"))
	if r.Body != nil {
		b, _ := io.ReadAll(r.Body)
		if len(b) > 0 {
			var m map[string]any
			json.Unmarshal(b, &m)
			p.sent = append(p.sent, m)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(p.status)
	io.WriteString(w, p.body)
}

func (p *deliveryPlugin) answer(status int, body string) {
	p.mu.Lock()
	p.status, p.body, p.requests, p.sent = status, body, nil, nil
	p.mu.Unlock()
}

func deliveryClient(t *testing.T) (*Client, *deliveryPlugin) {
	t.Helper()
	p := &deliveryPlugin{status: 200}
	srv := httptest.NewServer(p)
	t.Cleanup(srv.Close)
	c, err := New(srv.URL, "test")
	if err != nil {
		t.Fatal(err)
	}
	return c, p
}

const deliveryHex = "0123456789abcdef0123456789abcdef"

var admin = Auth{Token: "admin-key", DeviceID: "dev"}

func TestStartDelivery(t *testing.T) {
	ctx := context.Background()
	c, p := deliveryClient(t)
	o := DeliveryOrder{ItemId: deliveryHex, SourceId: deliveryHex, Segments: "mp4", StartTicks: 600_000_000,
		Video: &DeliveryVideo{Copy: false, Codec: "h264", MaxHeight: 720, MaxBitrate: 4_000_000, ToneMap: true, HeightAsked: true},
		Audio: &DeliveryAudio{Index: 2, Codec: "aac", Channels: 2}}

	p.answer(201, `{"Id":"`+deliveryHex+`","VideoCopied":false,"VideoCodec":"h264","Width":1280,"Height":720,"VideoBitrate":4000000,
		"ToneMapped":true,"AudioCopied":false,"AudioCodec":"aac","AudioChannels":2,"AudioIndex":2,"Segments":"mp4","Playlist":"#EXTM3U\n"}`)
	made, err := c.StartDelivery(ctx, admin, o)
	if err != nil {
		t.Fatal(err)
	}
	if made.Id != deliveryHex || made.Height != 720 || !made.ToneMapped || made.AudioIndex != 2 || made.Playlist != "#EXTM3U\n" {
		t.Errorf("made: %+v", made)
	}
	if len(p.requests) != 1 || p.requests[0] != "POST /Overfin/Deliveries" || !strings.Contains(p.auth[0], "admin-key") {
		t.Errorf("request: %v %v", p.requests, p.auth)
	}
	// What the plugin gets: the order by Jellyfin-style names, nothing more.
	sent := p.sent[0]
	if sent["ItemId"] != deliveryHex || sent["Segments"] != "mp4" || sent["StartTicks"] != float64(600_000_000) {
		t.Errorf("sent: %v", sent)
	}
	v, _ := sent["Video"].(map[string]any)
	if v["Codec"] != "h264" || v["MaxHeight"] != float64(720) || v["ToneMap"] != true || v["HeightAsked"] != true {
		t.Errorf("sent video: %v", v)
	}
	if len(sent) != 6 {
		t.Errorf("sent members: %v", sent)
	}
	// A copied picture and no audio: no Audio member, no codec.
	p.answer(200, `{"Id":"`+deliveryHex+`","VideoCopied":true}`)
	if _, err := c.StartDelivery(ctx, admin, DeliveryOrder{ItemId: deliveryHex, SourceId: deliveryHex, Segments: "ts",
		Video: &DeliveryVideo{Copy: true}}); err != nil {
		t.Fatal(err)
	}
	if _, ok := p.sent[0]["Audio"]; ok {
		t.Errorf("an Audio member for none: %v", p.sent[0])
	}
	if v, _ := p.sent[0]["Video"].(map[string]any); v["Codec"] != nil || v["Copy"] != true {
		t.Errorf("copied video sent: %v", v)
	}

	// Malformed answers.
	for _, body := range []string{`{"Id":"not-hex"}`, `{"Id":"` + strings.ToUpper(deliveryHex) + `"}`, `{}`, `not json`} {
		p.answer(200, body)
		if _, err := c.StartDelivery(ctx, admin, o); err == nil {
			t.Errorf("%s: no error", body)
		}
	}
	// Refusals: the plugin's closed code, else "refused".
	for body, code := range map[string]string{
		`{"Refused":"no-encoder"}`:                       "no-encoder",
		`{"Refused":"made-differs:height-cap"}`:          "made-differs:height-cap",
		`{"Refused":"<script>alert(1)</script>"}`:        "refused",
		`{"Refused":""}`:                                 "refused",
		`not json`:                                       "refused",
		`{"Refused":"` + strings.Repeat("a", 201) + `"}`: "refused",
	} {
		p.answer(409, body)
		_, err := c.StartDelivery(ctx, admin, o)
		var r *DeliveryRefused
		if !errors.As(err, &r) || r.Code != code {
			t.Errorf("409 %s: %v, want refused %q", body, err, code)
		}
	}
	// Any other status: a StatusError, the body not read into the error.
	p.answer(500, `{"secret":"path /media/x.mkv"}`)
	_, err = c.StartDelivery(ctx, admin, o)
	if !IsStatus(err, 500) || strings.Contains(err.Error(), "/media") {
		t.Errorf("500: %v", err)
	}
}

func TestDeliveryPingStopClear(t *testing.T) {
	ctx := context.Background()
	c, p := deliveryClient(t)

	p.answer(204, "")
	if err := c.PingDelivery(ctx, admin, deliveryHex); err != nil {
		t.Fatal(err)
	}
	if p.requests[0] != "POST /Overfin/Deliveries/"+deliveryHex+"/Ping" {
		t.Errorf("ping: %v", p.requests)
	}
	p.answer(404, "")
	if err := c.PingDelivery(ctx, admin, deliveryHex); !IsStatus(err, 404) {
		t.Errorf("ping, gone: %v", err)
	}
	// A malformed ID never leaves: gone for a ping, nothing for a stop.
	p.answer(204, "")
	for _, id := range []string{"", "../x", deliveryHex + "0", strings.ToUpper(deliveryHex), "0123456789abcdef0123456789abcdeg"} {
		if err := c.PingDelivery(ctx, admin, id); !IsStatus(err, 404) {
			t.Errorf("ping %q: %v", id, err)
		}
		if err := c.StopDelivery(ctx, admin, id); err != nil {
			t.Errorf("stop %q: %v", id, err)
		}
	}
	if len(p.requests) != 0 {
		t.Errorf("requests for malformed IDs: %v", p.requests)
	}

	if err := c.StopDelivery(ctx, admin, deliveryHex); err != nil || p.requests[0] != "DELETE /Overfin/Deliveries/"+deliveryHex {
		t.Errorf("stop: %v %v", err, p.requests)
	}
	p.answer(404, "")
	if err := c.StopDelivery(ctx, admin, deliveryHex); err != nil {
		t.Errorf("stop, already gone: %v", err)
	}
	p.answer(500, "")
	if err := c.StopDelivery(ctx, admin, deliveryHex); !IsStatus(err, 500) {
		t.Errorf("stop, failing: %v", err)
	}
	p.answer(204, "")
	if err := c.ClearDeliveries(ctx, admin); err != nil || p.requests[0] != "DELETE /Overfin/Deliveries" {
		t.Errorf("clear: %v %v", err, p.requests)
	}
}

func TestPluginEncoders(t *testing.T) {
	c, p := deliveryClient(t)
	p.answer(200, `{"Video":["h264","hevc"],"Audio":["aac"],"Hardware":true,"ToneMapping":false}`)
	e, err := c.Encoders(context.Background(), admin)
	if err != nil || strings.Join(e.Video, ",") != "h264,hevc" || !e.Hardware || e.ToneMapping {
		t.Errorf("encoders: %+v %v", e, err)
	}
	if p.requests[0] != "GET /Overfin/Encoders" {
		t.Errorf("request: %v", p.requests)
	}
	p.answer(500, "")
	if _, err := c.Encoders(context.Background(), admin); err == nil {
		t.Error("encoders, failing: no error")
	}
}
