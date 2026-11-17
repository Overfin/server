package api

import (
	"net/http"
	"strconv"
	"testing"
)

// A 206's headers pass checkPartial only for a part of the range asked,
// its length end − start + 1, and Content-Length (when given) that.
func FuzzCheckPartial(f *testing.F) {
	f.Add("bytes=0-9", "bytes 0-9/100", "10")
	f.Add("bytes=-10", "bytes 90-99/100", "")
	f.Add("bytes=5-", "bytes 5-9/*", "5")
	f.Add("bytes=0-1,5-9", "bytes 0-1/10", "2")
	f.Fuzz(func(t *testing.T, rangeHeader, contentRange, contentLength string) {
		br, ok := parseRange(rangeHeader)
		if !ok {
			return
		}
		h := http.Header{}
		h.Set("Content-Range", contentRange)
		if contentLength != "" {
			h.Set("Content-Length", contentLength)
		}
		n, ok := checkPartial(rangeHeader, br, h)
		if !ok {
			return
		}
		m := contentRangeRe.FindStringSubmatch(contentRange)
		if m == nil {
			t.Fatalf("passed %q", contentRange)
		}
		start, _ := strconv.ParseInt(m[1], 10, 64)
		end, _ := strconv.ParseInt(m[2], 10, 64)
		switch {
		case n != end-start+1 || n < 1:
			t.Fatalf("%q: length %d", contentRange, n)
		case contentLength != "" && contentLength != strconv.FormatInt(n, 10):
			t.Fatalf("%q with Content-Length %q", contentRange, contentLength)
		case br.suffix == 0 && (start != br.first || br.last >= 0 && end > br.last):
			t.Fatalf("%q for %q", contentRange, rangeHeader)
		case m[3] != "*":
			if total, _ := strconv.ParseInt(m[3], 10, 64); end >= total || br.suffix > 0 && start != max(total-br.suffix, 0) {
				t.Fatalf("%q for %q", contentRange, rangeHeader)
			}
		case br.suffix > 0:
			t.Fatalf("a suffix range passed with an unknown total: %q", contentRange)
		}
	})
}
