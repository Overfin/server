package api

import "testing"

// A device is named by its browser and system, with no word of any
// language between them.
func TestDeviceName(t *testing.T) {
	for ua, want := range map[string]string{
		"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36": "Chrome · Linux",
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:131.0) Gecko/20100101 Firefox/131.0":                      "Firefox · Windows",
		"Mozilla/5.0 (X11; Linux x86_64)": "Linux",
		"curl/8.0":                        "Unknown device",
	} {
		if got := deviceName(ua); got != want {
			t.Errorf("%q: %q, want %q", ua, got, want)
		}
	}
}
