package site

import "testing"

func TestWithOrigins(t *testing.T) {
	got := WithOrigins("default-src 'self'; media-src 'self' blob:; connect-src 'self'; img-src 'self'", []string{"https://b.example.org", "http://10.1.2.3:8080"})
	want := "default-src 'self'; media-src 'self' blob: https://b.example.org http://10.1.2.3:8080; connect-src 'self' https://b.example.org http://10.1.2.3:8080; img-src 'self'"
	if got != want {
		t.Fatalf("\n got %s\nwant %s", got, want)
	}
	if got := WithOrigins("default-src 'self'", nil); got != "default-src 'self'" {
		t.Fatalf("no origins: %s", got)
	}
}
