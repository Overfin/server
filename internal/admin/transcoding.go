package admin

import (
	"context"
	"net/http"
	"regexp"

	"github.com/overfin/server/internal/gpu"
)

// Transcoding is where the panel chooses the GPU Overfin's Jellyfin
// transcodes with, after testing it.
type Transcoding interface {
	// Transcoding is the GPU family chosen ("" for none yet), and its
	// device.
	Transcoding(ctx context.Context) (family, device string, err error)
	// TestTranscoding starts trying a family on a few videos, in the
	// background; the settings are put back after.
	TestTranscoding(ctx context.Context, family, device string) error
	// TranscodingTest is how the last test went, or goes; nil before any.
	TranscodingTest() *GPUTest
	// SetTranscoding saves the family chosen and applies it.
	SetTranscoding(ctx context.Context, family, device string) error
	// Previews is whether Jellyfin makes seek-bar previews (off unless
	// turned on); SetPreviews saves it and applies it.
	Previews(ctx context.Context) (bool, error)
	SetPreviews(ctx context.Context, on bool) error
}

// GPUTest is a GPU test, running or done.
type GPUTest struct {
	Family  string       `json:"family"`
	Device  string       `json:"device"`
	Running bool         `json:"running"`
	Total   int          `json:"total"` // videos to test, once known
	Results []gpu.Result `json:"results"`
	Error   string       `json:"error,omitempty"`
}

// gpuTestOf is a test as the panel reads it: before any, one not running;
// its results a list, even empty.
func gpuTestOf(t *GPUTest) GPUTest {
	var out GPUTest
	if t != nil {
		out = *t
	}
	if out.Results == nil {
		out.Results = []gpu.Result{}
	}
	return out
}

// renderDevice is a GPU's render device on Linux.
var renderDevice = regexp.MustCompile(`^/dev/dri/renderD[0-9]{3}$`)

// gpuChoice reads and checks a family and its device from a request.
func gpuChoice(w http.ResponseWriter, r *http.Request) (gpu.Family, string, bool) {
	var req struct {
		Family string `json:"family"`
		Device string `json:"device"`
	}
	if !decode(w, r, &req) {
		return gpu.Family{}, "", false
	}
	f, ok := gpu.Find(req.Family)
	device := ""
	switch {
	case !ok:
		writeJSON(w, http.StatusBadRequest, map[string]string{"type": "invalid-request", "detail": "Overfin doesn't know that GPU."})
		return gpu.Family{}, "", false
	case f.Device && !renderDevice.MatchString(req.Device):
		writeJSON(w, http.StatusBadRequest, map[string]string{"type": "invalid-request",
			"detail": "The device is a render device, such as " + gpu.DefaultDevice + "."})
		return gpu.Family{}, "", false
	case f.Device:
		device = req.Device
	}
	return f, device, true
}

func (p *Panel) transcoding(w http.ResponseWriter, r *http.Request, s *session) {
	if p.Transcoding == nil {
		http.NotFound(w, r)
		return
	}
	family, device, err := p.Transcoding.Transcoding(r.Context())
	if err != nil {
		p.upstreamProblem(w, err, "reading the GPU chosen")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"family": family, "device": device,
		"defaultDevice": gpu.DefaultDevice, "families": gpu.Families})
}

func (p *Panel) testTranscoding(w http.ResponseWriter, r *http.Request, s *session) {
	if p.Transcoding == nil {
		http.NotFound(w, r)
		return
	}
	f, device, ok := gpuChoice(w, r)
	if !ok {
		return
	}
	if err := p.Transcoding.TestTranscoding(r.Context(), f.ID, device); err != nil {
		p.upstreamProblem(w, err, "testing a GPU")
		return
	}
	p.Logger.Info("a GPU is being tested in the control panel", "gpu", f.ID, "by", s.logID)
	writeJSON(w, http.StatusAccepted, gpuTestOf(p.Transcoding.TranscodingTest()))
}

// transcodingTest is how the last test went, or goes.
func (p *Panel) transcodingTest(w http.ResponseWriter, r *http.Request, s *session) {
	if p.Transcoding == nil {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, http.StatusOK, gpuTestOf(p.Transcoding.TranscodingTest()))
}

func (p *Panel) setTranscoding(w http.ResponseWriter, r *http.Request, s *session) {
	if p.Transcoding == nil {
		http.NotFound(w, r)
		return
	}
	f, device, ok := gpuChoice(w, r)
	if !ok {
		return
	}
	if err := p.Transcoding.SetTranscoding(r.Context(), f.ID, device); err != nil {
		p.upstreamProblem(w, err, "choosing a GPU")
		return
	}
	p.Logger.Info("a GPU was chosen in the control panel", "gpu", f.ID, "by", s.logID)
	w.WriteHeader(http.StatusNoContent)
}

// previewsJSON is whether Overfin's Jellyfin makes seek-bar previews.
type previewsJSON struct {
	On bool `json:"on"`
}

func (p *Panel) previews(w http.ResponseWriter, r *http.Request, s *session) {
	if p.Transcoding == nil {
		http.NotFound(w, r)
		return
	}
	on, err := p.Transcoding.Previews(r.Context())
	if err != nil {
		p.upstreamProblem(w, err, "reading whether previews are made")
		return
	}
	writeJSON(w, http.StatusOK, previewsJSON{On: on})
}

func (p *Panel) setPreviews(w http.ResponseWriter, r *http.Request, s *session) {
	if p.Transcoding == nil {
		http.NotFound(w, r)
		return
	}
	var req previewsJSON
	if !decode(w, r, &req) {
		return
	}
	if err := p.Transcoding.SetPreviews(r.Context(), req.On); err != nil {
		p.upstreamProblem(w, err, "turning previews on or off")
		return
	}
	p.Logger.Info("seek-bar previews set in the control panel", "on", req.On)
	writeJSON(w, http.StatusOK, req)
}
