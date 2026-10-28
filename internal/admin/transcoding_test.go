package admin

import (
	"context"
	"strings"
	"testing"

	"github.com/overfin/server/internal/gpu"
)

// memTranscoding remembers the GPU chosen, what was tested, and previews.
type memTranscoding struct {
	family, device string
	previews       bool
	tested         []string
	last           *GPUTest
}

func (m *memTranscoding) Transcoding(context.Context) (string, string, error) {
	return m.family, m.device, nil
}

func (m *memTranscoding) TestTranscoding(_ context.Context, family, device string) error {
	m.tested = append(m.tested, family+" "+device)
	m.last = &GPUTest{Family: family, Total: 1, Results: []gpu.Result{{Kind: "H.264", OK: true, Speed: 12}}}
	return nil
}

func (m *memTranscoding) TranscodingTest() *GPUTest { return m.last }

func (m *memTranscoding) SetTranscoding(_ context.Context, family, device string) error {
	m.family, m.device = family, device
	return nil
}

func (m *memTranscoding) Previews(context.Context) (bool, error) { return m.previews, nil }

func (m *memTranscoding) SetPreviews(_ context.Context, on bool) error {
	m.previews = on
	return nil
}

func TestPreviewsRoutes(t *testing.T) {
	p, idp, _ := newPanel(t)
	tr := &memTranscoding{}
	p.Transcoding = tr
	b := &browser{h: p.Handler(), cookies: map[string]string{}}
	if rec := b.do("PUT", "/admin/api/previews", `{"on":true}`, nil); rec.Code != 401 || tr.previews {
		t.Fatalf("before signing in: %d", rec.Code)
	}
	signIn(t, b, idp, []string{"overfin-admins"})
	if rec := b.do("GET", "/admin/api/previews", "", nil); rec.Body.String() != `{"on":false}`+"\n" {
		t.Errorf("off by default: %s", rec.Body)
	}
	if rec := b.do("PUT", "/admin/api/previews", `{"on":true}`, nil); rec.Code != 200 || !tr.previews {
		t.Errorf("turned on: %d %s", rec.Code, rec.Body)
	}
	if rec := b.do("PUT", "/admin/api/previews", `{"on":"yes"}`, nil); rec.Code != 400 {
		t.Errorf("not a boolean: %d", rec.Code)
	}
}

func TestTranscodingRoutes(t *testing.T) {
	p, idp, _ := newPanel(t)
	tr := &memTranscoding{}
	p.Transcoding = tr
	b := &browser{h: p.Handler(), cookies: map[string]string{}}
	if rec := b.do("POST", "/admin/api/transcoding/test", `{"family":"software"}`, nil); rec.Code != 401 {
		t.Fatalf("before signing in: %d", rec.Code)
	}
	signIn(t, b, idp, []string{"overfin-admins"})

	// Before any test: one not running, not null.
	if rec := b.do("GET", "/admin/api/transcoding/test", "", nil); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"running":false`) ||
		!strings.Contains(rec.Body.String(), `"results":[]`) {
		t.Errorf("before any test: %d %s", rec.Code, rec.Body)
	}
	if rec := b.do("GET", "/admin/api/transcoding", "", nil); !strings.Contains(rec.Body.String(), `"id":"nvidia-pascal"`) {
		t.Errorf("families: %s", rec.Body)
	}
	for body, want := range map[string]string{
		`{"family":"nope"}`:                                    "doesn't know",
		`{"family":"intel-arc","device":"/etc/passwd"}`:        "render device",
		`{"family":"intel-arc","device":"/dev/dri/renderD12"}`: "render device",
	} {
		if rec := b.do("POST", "/admin/api/transcoding/test", body, nil); rec.Code != 400 || !strings.Contains(rec.Body.String(), want) {
			t.Errorf("%s: %d %s", body, rec.Code, rec.Body)
		}
	}
	// A family that needs no device gets none, whatever was sent.
	if rec := b.do("POST", "/admin/api/transcoding/test", `{"family":"nvidia-ada","device":"/dev/dri/renderD128"}`, nil); rec.Code != 202 ||
		tr.tested[0] != "nvidia-ada " {
		t.Errorf("test: %d %s %v", rec.Code, rec.Body, tr.tested)
	}
	if rec := b.do("GET", "/admin/api/transcoding/test", "", nil); !strings.Contains(rec.Body.String(), `"speed":12`) {
		t.Errorf("progress: %s", rec.Body)
	}
	if rec := b.do("PUT", "/admin/api/transcoding", `{"family":"intel-arc","device":"/dev/dri/renderD129"}`, nil); rec.Code != 204 ||
		tr.family != "intel-arc" || tr.device != "/dev/dri/renderD129" {
		t.Errorf("use: %d %+v", rec.Code, tr)
	}
}
