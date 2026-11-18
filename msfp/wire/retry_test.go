package wire

import "testing"

// retryAfter counts only from 1 to 3,600, as an integer.
func TestRetryAfterOf(t *testing.T) {
	for body, want := range map[string]int{`{"retryAfter":5}`: 5, `{"retryAfter":3600}`: 3600, `{"retryAfter":0}`: 0,
		`{"retryAfter":3601}`: 0, `{"retryAfter":1.5}`: 0, `{"retryAfter":"5"}`: 0, `{}`: 0, `x`: 0} {
		if got := RetryAfter([]byte(body)); got != want {
			t.Errorf("%s: %d", body, got)
		}
	}
}
