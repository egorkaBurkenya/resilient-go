package resilient

import (
	"net/http"
	"time"
)

// Option configures a Client.
type Option func(*config)

type config struct {
	baseURL          string
	rps              float64
	burst            int
	maxRetries       int
	initialBackoff   time.Duration
	adaptiveCooldown time.Duration
	maxResponseSize  int64
	timeout          time.Duration
	retryableStatus  map[int]bool
	httpClient       *http.Client

	onError       func(statusCode int, req *http.Request)
	onSuccess     func(req *http.Request, resp *http.Response)
	onRateLimited func(req *http.Request)

	requestHook  func(req *http.Request)
	responseHook func(resp *http.Response)

	retryPolicy RetryPolicy
}

// RetryPolicy decides whether a request should be retried.
// It receives the attempt number (0-based), the response (may be nil on
// network errors), and the error. Return true to retry, false to stop.
type RetryPolicy func(attempt int, resp *http.Response, err error) bool

func defaultConfig() *config {
	return &config{
		rps:              0, // no rate limiting by default
		burst:            1,
		maxRetries:       3,
		initialBackoff:   2 * time.Second,
		adaptiveCooldown: 5 * time.Minute,
		maxResponseSize:  10 * 1024 * 1024, // 10 MB
		timeout:          30 * time.Second,
		retryableStatus: map[int]bool{
			http.StatusTooManyRequests:     true,
			http.StatusServiceUnavailable:  true,
		},
	}
}

// WithBaseURL sets the base URL prefix for convenience methods (Get, Post, DoJSON).
func WithBaseURL(url string) Option {
	return func(c *config) { c.baseURL = url }
}

// WithRateLimit sets the token bucket rate limit in requests per second and burst size.
func WithRateLimit(rps float64, burst int) Option {
	return func(c *config) {
		c.rps = rps
		if burst > 0 {
			c.burst = burst
		}
	}
}

// WithRetry sets the maximum number of retries and initial backoff duration.
// Backoff doubles on each attempt with jitter added.
func WithRetry(maxRetries int, initialBackoff time.Duration) Option {
	return func(c *config) {
		c.maxRetries = maxRetries
		c.initialBackoff = initialBackoff
	}
}

// WithAdaptive sets the cooldown duration for adaptive rate reduction.
// When a rate-limit response is received, the rate is halved and restored
// after this duration.
func WithAdaptive(cooldown time.Duration) Option {
	return func(c *config) { c.adaptiveCooldown = cooldown }
}

// WithTimeout sets the HTTP client timeout.
func WithTimeout(d time.Duration) Option {
	return func(c *config) { c.timeout = d }
}

// WithMaxResponseSize sets the maximum response body size in bytes.
func WithMaxResponseSize(n int64) Option {
	return func(c *config) { c.maxResponseSize = n }
}

// WithRetryableStatus sets the HTTP status codes that trigger a retry.
// This replaces the default set (429, 503).
func WithRetryableStatus(codes ...int) Option {
	return func(c *config) {
		c.retryableStatus = make(map[int]bool, len(codes))
		for _, code := range codes {
			c.retryableStatus[code] = true
		}
	}
}

// WithHTTPClient sets a custom underlying *http.Client.
// The timeout option is ignored when a custom client is provided.
func WithHTTPClient(hc *http.Client) Option {
	return func(c *config) { c.httpClient = hc }
}

// WithOnError sets a callback invoked on non-retryable error responses.
func WithOnError(fn func(statusCode int, req *http.Request)) Option {
	return func(c *config) { c.onError = fn }
}

// WithOnSuccess sets a callback invoked on successful (2xx) responses.
func WithOnSuccess(fn func(req *http.Request, resp *http.Response)) Option {
	return func(c *config) { c.onSuccess = fn }
}

// WithOnRateLimited sets a callback invoked when a rate-limit response is received.
func WithOnRateLimited(fn func(req *http.Request)) Option {
	return func(c *config) { c.onRateLimited = fn }
}

// WithRequestHook sets a hook called before each request is sent.
func WithRequestHook(fn func(req *http.Request)) Option {
	return func(c *config) { c.requestHook = fn }
}

// WithResponseHook sets a hook called after each response is received.
func WithResponseHook(fn func(resp *http.Response)) Option {
	return func(c *config) { c.responseHook = fn }
}

// WithRetryPolicy sets a custom retry policy. When set, it takes precedence
// over the default status-code-based retry logic.
func WithRetryPolicy(p RetryPolicy) Option {
	return func(c *config) { c.retryPolicy = p }
}
