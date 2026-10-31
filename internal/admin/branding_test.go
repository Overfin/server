package admin

import (
	"bytes"
	"context"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestBranding(t *testing.T) {
	p, b, _, _ := signedInPanel(t)
	if rec := b.do("GET", "/admin/api/branding", "", nil); rec.Code != 200 || rec.Body.String() != `{"on":false,"name":"","files":{}}`+"\n" {
		t.Errorf("before: %d %s", rec.Code, rec.Body)
	}
	for body, want := range map[string]string{
		`{"on":true,"name":""}`:                                "Give your site's name",
		`{"on":true,"name":"<b>Riverside</b>"}`:                "plain text",
		`{"on":true,"name":"` + strings.Repeat("z", 41) + `"}`: "40 characters",
	} {
		if rec := b.do("PUT", "/admin/api/branding", body, nil); rec.Code != 400 || !strings.Contains(rec.Body.String(), want) {
			t.Errorf("%s: %d %s", body, rec.Code, rec.Body)
		}
	}
	if rec := b.do("PUT", "/admin/api/branding", `{"on":true,"name":" Riverside "}`, nil); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"on":true,"name":"Riverside"`) {
		t.Errorf("on: %d %s", rec.Code, rec.Body)
	}

	// A file goes as it is, with its image type; nothing else is taken.
	upload := func(path, contentType, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("PUT", path, strings.NewReader(body))
		req.RemoteAddr = "127.0.0.1:50000"
		req.Header.Set("Content-Type", contentType)
		for k, v := range b.cookies {
			req.Header.Add("Cookie", k+"="+v)
		}
		rec := httptest.NewRecorder()
		p.Handler().ServeHTTP(rec, req)
		return rec
	}
	svg := `<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`
	if rec := upload(brandingFiles+"banner-dark", "text/html", svg); rec.Code != 415 {
		t.Errorf("not an image: %d", rec.Code)
	}
	if rec := upload(brandingFiles+"other", "image/svg+xml", svg); rec.Code != 404 {
		t.Errorf("unknown kind: %d", rec.Code)
	}
	// No SVG, whatever type it comes as: emails can't show it.
	for _, ct := range []string{"image/svg+xml", "image/png"} {
		if rec := upload(brandingFiles+"banner-dark", ct, svg); rec.Code != 400 || !strings.Contains(rec.Body.String(), "SVG") {
			t.Errorf("SVG as %s: %d %s", ct, rec.Code, rec.Body)
		}
	}
	// Not a picture Overfin reads: refused, whatever it says it is.
	if rec := upload(brandingFiles+"banner-dark", "image/png", "\x89PNG\r\n\x1a\n"+strings.Repeat("x", 64)); rec.Code != 400 {
		t.Errorf("a fake PNG: %d", rec.Code)
	}
	// A picture: made Overfin's, a PNG no wider than it's ever shown.
	if rec := upload(brandingFiles+"banner-dark", "image/jpeg", picture(t, 3000, 200, "jpeg")); rec.Code != 200 ||
		!strings.Contains(rec.Body.String(), `"banner-dark":"/admin/api/branding/files/banner-dark?v=`) {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body)
	}
	rec := b.do("GET", brandingFiles+"banner-dark", "", nil)
	cfg, format, err := image.DecodeConfig(bytes.NewReader(rec.Body.Bytes()))
	if rec.Code != 200 || err != nil || format != "png" || cfg.Width != 1200 || rec.Header().Get("Content-Type") != "image/png" ||
		!strings.Contains(rec.Header().Get("Content-Security-Policy"), "sandbox") {
		t.Errorf("shown: %d %s %d %v", rec.Code, format, cfg.Width, rec.Header())
	}
	if rec := upload(brandingFiles+"logo", "image/png", strings.Repeat("x", maxBrandingFile+1)); rec.Code != 413 {
		t.Errorf("too big: %d", rec.Code)
	}
	if rec := b.do("DELETE", brandingFiles+"banner-dark", "", nil); rec.Code != 200 || strings.Contains(rec.Body.String(), "banner-dark") {
		t.Errorf("removed: %d %s", rec.Code, rec.Body)
	}
}

// picture is a w by h picture, as a "png" or a "jpeg" file.
func picture(t *testing.T, w, h int, format string) string {
	t.Helper()
	var b bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	if format == "png" {
		png.Encode(&b, img)
	} else {
		jpeg.Encode(&b, img, nil)
	}
	return b.String()
}

// Branding files kept as they were uploaded become Overfin's at start: a
// JPEG as wide as it came, a PNG; one Overfin can't read, removed; one
// prepared already, left as it is.
func TestPrepareBranding(t *testing.T) {
	ctx := context.Background()
	_, _, db, _ := signedInPanel(t)
	long := time.Now().Add(-time.Hour)
	db.SetBrandingFile(ctx, "banner-light", "image/jpeg", []byte(picture(t, 2400, 100, "jpeg")), long)
	db.SetBrandingFile(ctx, "banner-dark", "image/gif", []byte("GIF89a......"), long)
	db.SetBrandingFile(ctx, "logo", "image/png", []byte(picture(t, 256, 256, "png")), long)
	if err := PrepareBranding(ctx, db, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatal(err)
	}
	light, ok, _ := db.BrandingFileOf(ctx, "banner-light")
	cfg, format, err := image.DecodeConfig(bytes.NewReader(light.Data))
	if !ok || err != nil || format != "png" || light.ContentType != "image/png" || cfg.Width != 1200 {
		t.Errorf("banner: %v %s %d %s", ok, format, cfg.Width, light.ContentType)
	}
	if _, ok, _ := db.BrandingFileOf(ctx, "banner-dark"); ok {
		t.Error("a GIF stayed")
	}
	if logo, _, _ := db.BrandingFileOf(ctx, "logo"); !logo.Updated.Equal(long.Truncate(time.Second)) {
		t.Errorf("a prepared logo was made again: %v", logo.Updated)
	}
}
