package wire

// MaxRetryAfter is the longest a signed refusal may ask a caller to wait,
// in seconds: an hour (section 8.3).
const MaxRetryAfter = 3600

// RetryAfter is a signed refusal's retryAfter, 0 when it has none in
// range (1 to 3,600 seconds).
func RetryAfter(body []byte) int {
	var p struct {
		RetryAfter int `json:"retryAfter"` // a fraction or a string: not one
	}
	if DecodeAnswer(body, &p) != nil || p.RetryAfter < 1 || p.RetryAfter > MaxRetryAfter {
		return 0
	}
	return p.RetryAfter
}
