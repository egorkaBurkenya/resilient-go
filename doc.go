// Package resilient provides a production-ready HTTP client with built-in
// rate limiting, retry with exponential backoff, and adaptive rate reduction.
//
// It wraps the standard net/http client and adds:
//   - Proactive rate limiting via a token bucket (golang.org/x/time/rate)
//   - Automatic retry with exponential backoff and jitter
//   - Adaptive rate reduction on rate-limit responses (halves rate, auto-restores)
//   - Retry-After header parsing (seconds and HTTP-date formats)
//   - Atomic stats tracking (total requests, errors, rate-limited count)
//   - Configurable callbacks and request/response hooks
//   - Thread-safe concurrent usage
//
// Configuration uses the functional options pattern:
//
//	client := resilient.New(
//	    resilient.WithBaseURL("https://api.example.com"),
//	    resilient.WithRateLimit(5.0, 2),
//	    resilient.WithRetry(3, 2*time.Second),
//	    resilient.WithAdaptive(5*time.Minute),
//	)
//	defer client.Close()
//
//	body, status, err := client.Get(ctx, "/users")
package resilient
