package images

import (
	"bytes"
	"errors"
	"image"
	"image/png"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"
)

// The test binary is its own image worker, as the backend's program is:
// started again with workerEnv set. workerTestEnv makes it misbehave.
const workerTestEnv = "OVERFIN_IMAGE_WORKER_TEST"

func TestMain(m *testing.M) {
	if os.Getenv(workerEnv) != "" {
		switch os.Getenv(workerTestEnv) {
		case "crash":
			readRequest(os.Stdin)
			os.Exit(3)
		case "hang":
			readRequest(os.Stdin)
			time.Sleep(time.Hour)
		case "eat":
			// Past its memory cap: it must fail, not grow. Surviving, it
			// answers an image 12345 pixels wide.
			capMemory()
			readRequest(os.Stdin)
			b := make([]byte, 2<<30)
			for i := 0; i < len(b); i += 4096 {
				b[i] = 1
			}
			writeAnswer(os.Stdout, statusMade, 12345, [][]byte{b[:1]})
			os.Exit(0)
		}
		RunWorker()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func testPicture(t *testing.T, w, h int) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestWorkersMakeImages(t *testing.T) {
	p, err := Workers(2, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	raw := testPicture(t, 800, 400)
	outs, width, err := p.Transform(raw, []int{200, 0}, "image/jpeg")
	if err != nil || width != 800 || len(outs) != 2 {
		t.Fatalf("%d outputs, width %d, %v", len(outs), width, err)
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(outs[0]))
	if err != nil || format != "jpeg" || cfg.Width != 200 {
		t.Errorf("first: %s %dpx %v", format, cfg.Width, err)
	}
	want, _, _ := InProcess(raw, []int{200, 0}, "image/jpeg")
	if !bytes.Equal(outs[1], want[1]) {
		t.Error("a worker's image isn't what the backend would make")
	}
	// Not an image Overfin takes: refused, and the worker goes on.
	if _, _, err := p.Transform([]byte("<svg/>"), []int{0}, "image/png"); !errors.Is(err, ErrUnsafe) {
		t.Errorf("an SVG: %v", err)
	}
	if _, _, err := p.Transform(raw, []int{100}, "image/png"); err != nil {
		t.Errorf("after a refusal: %v", err)
	}
}

// A worker that dies, or takes too long, is replaced; its image is refused.
func TestWorkersFail(t *testing.T) {
	p, err := Workers(1, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	raw := testPicture(t, 10, 10)

	t.Setenv(workerTestEnv, "crash")
	if _, _, err := p.Transform(raw, []int{0}, "image/png"); !errors.Is(err, ErrUnsafe) {
		t.Errorf("a crashed worker: %v", err)
	}
	t.Setenv(workerTestEnv, "hang")
	p.timeout = 500 * time.Millisecond
	start := time.Now()
	if _, _, err := p.Transform(raw, []int{0}, "image/png"); !errors.Is(err, ErrUnsafe) || time.Since(start) > 5*time.Second {
		t.Errorf("a hung worker: %v after %v", err, time.Since(start))
	}
	if !raceBuild {
		t.Setenv(workerTestEnv, "eat")
		p.timeout = 20 * time.Second
		if _, _, err := p.Transform(raw, []int{0}, "image/png"); !errors.Is(err, ErrUnsafe) {
			t.Errorf("a worker past its memory: %v", err)
		}
	}
	t.Setenv(workerTestEnv, "")
	p.timeout = workerTimeout
	if _, _, err := p.Transform(raw, []int{0}, "image/png"); err != nil {
		t.Errorf("a new worker after both: %v", err)
	}
}
