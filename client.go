package resilient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/time/rate"
)

// Stats holds atomic request counters.
type Stats struct {
	TotalRequests uint64
	TotalErrors   uint64
	RateLimited   uint64
}

// StatsProvider exposes metrics for external collectors (Prometheus, OTel, etc.).
type StatsProvider interface {
	Stats() Stats
}

// Client is a resilient HTTP client with rate limiting, retry, and adaptive backoff.
type Client struct {
	httpClient *http.Client
	limiter    *rate.Limiter
	cfg        *config

	mu            sync.Mutex
	originalRate  rate.Limit
	adaptiveTimer *time.Timer
	closed        bool

	totalReqs   atomic.Uint64
	totalErrors atomic.Uint64
	rateLimited atomic.Uint64
}

// Compile-time interface check.
var _ StatsProvider = (*Client)(nil)

// New creates a new resilient Client with the given options.
func New(opts ...Option) *Client {
	cfg := defaultConfig()
	for _, o := range opts {
		o(cfg)
	}

	hc := cfg.httpClient
	if hc == nil {
		hc = &http.Client{Timeout: cfg.timeout}
	}

	var lim *rate.Limiter
	if cfg.rps > 0 {
		lim = rate.NewLimiter(rate.Limit(cfg.rps), cfg.burst)
	}

	return &Client{
		httpClient:   hc,
		limiter:      lim,
		cfg:          cfg,
		originalRate: rate.Limit(cfg.rps),
	}
}

// Close releases resources held by the client (adaptive timer, etc.).
func (c *Client) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	if c.adaptiveTimer != nil {
		c.adaptiveTimer.Stop()
		c.adaptiveTimer = nil
	}
}

// Stats returns a snapshot of request statistics.
func (c *Client) Stats() Stats {
	return Stats{
		TotalRequests: c.totalReqs.Load(),
		TotalErrors:   c.totalErrors.Load(),
		RateLimited:   c.rateLimited.Load(),
	}
}

// SetRateLimit dynamically adjusts the rate limit.
func (c *Client) SetRateLimit(rps float64, burst int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	newRate := rate.Limit(rps)
	if c.limiter == nil {
		c.limiter = rate.NewLimiter(newRate, burst)
	} else {
		c.limiter.SetLimit(newRate)
		c.limiter.SetBurst(burst)
	}
	c.originalRate = newRate
}

// Do executes an HTTP request with rate limiting, retry, and adaptive backoff.
// It returns the response body, HTTP status code, and any error.
func (c *Client) Do(ctx context.Context, req *http.Request) ([]byte, int, error) {
	if err := c.waitRateLimit(ctx); err != nil {
		return nil, 0, fmt.Errorf("resilient: rate limit wait: %w", err)
	}

	c.totalReqs.Add(1)

	var (
		lastErr    error
		lastStatus int
		bodyBytes  []byte
	)

	// Capture the body for retries if it's non-nil.
	if req.Body != nil && req.Body != http.NoBody {
		var err error
		bodyBytes, err = io.ReadAll(req.Body)
		req.Body.Close()
		if err != nil {
			return nil, 0, fmt.Errorf("resilient: read request body: %w", err)
		}
	}

	for attempt := 0; attempt <= c.cfg.maxRetries; attempt++ {
		if attempt > 0 {
			backoff := c.backoffDuration(attempt, lastStatus)
			select {
			case <-ctx.Done():
				return nil, 0, ctx.Err()
			case <-time.After(backoff):
			}
			if err := c.waitRateLimit(ctx); err != nil {
				return nil, 0, fmt.Errorf("resilient: rate limit wait: %w", err)
			}
		}

		// Clone the request for each attempt.
		clone := req.Clone(ctx)
		if bodyBytes != nil {
			clone.Body = io.NopCloser(bytes.NewReader(bodyBytes))
			clone.ContentLength = int64(len(bodyBytes))
		}

		if c.cfg.requestHook != nil {
			c.cfg.requestHook(clone)
		}

		resp, err := c.httpClient.Do(clone)
		if err != nil {
			c.totalErrors.Add(1)
			lastErr = fmt.Errorf("resilient: http request: %w", err)
			if c.shouldRetry(attempt, nil, err) {
				continue
			}
			return nil, 0, lastErr
		}

		if c.cfg.responseHook != nil {
			c.cfg.responseHook(resp)
		}

		respBody, err := io.ReadAll(io.LimitReader(resp.Body, c.cfg.maxResponseSize))
		resp.Body.Close()
		if err != nil {
			c.totalErrors.Add(1)
			return nil, resp.StatusCode, fmt.Errorf("resilient: read response: %w", err)
		}

		lastStatus = resp.StatusCode

		if c.shouldRetry(attempt, resp, nil) {
			if resp.StatusCode == http.StatusTooManyRequests {
				c.rateLimited.Add(1)
				if c.cfg.onRateLimited != nil {
					c.cfg.onRateLimited(req)
				}
			}
			c.totalErrors.Add(1)
			if c.cfg.onError != nil {
				c.cfg.onError(resp.StatusCode, req)
			}
			c.reduceRateLimit()
			// Store retry-after for next iteration's backoff calc.
			if ra := parseRetryAfter(resp.Header.Get("Retry-After")); ra > 0 {
				lastStatus = resp.StatusCode // keep for backoff
			}
			lastErr = fmt.Errorf("resilient: HTTP %d on %s %s", resp.StatusCode, req.Method, req.URL)
			continue
		}

		if resp.StatusCode >= 400 {
			c.totalErrors.Add(1)
			if resp.StatusCode == http.StatusTooManyRequests {
				c.rateLimited.Add(1)
			}
			if c.cfg.onError != nil {
				c.cfg.onError(resp.StatusCode, req)
			}
			return respBody, resp.StatusCode, fmt.Errorf("resilient: HTTP %d: %s", resp.StatusCode, string(respBody))
		}

		if c.cfg.onSuccess != nil {
			c.cfg.onSuccess(req, resp)
		}
		return respBody, resp.StatusCode, nil
	}

	return nil, lastStatus, fmt.Errorf("resilient: max retries (%d) exceeded: %w", c.cfg.maxRetries, lastErr)
}

// Get performs a GET request to baseURL+path.
func (c *Client) Get(ctx context.Context, path string, headers ...map[string]string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.cfg.baseURL+path, nil)
	if err != nil {
		return nil, 0, err
	}
	applyHeaders(req, headers)
	return c.Do(ctx, req)
}

// Post performs a POST request to baseURL+path with the given body.
func (c *Client) Post(ctx context.Context, path string, contentType string, body io.Reader, headers ...map[string]string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.baseURL+path, body)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", contentType)
	applyHeaders(req, headers)
	return c.Do(ctx, req)
}

// DoJSON marshals reqBody as JSON, sends a request, and unmarshals the response into respBody.
func (c *Client) DoJSON(ctx context.Context, method, path string, reqBody, respBody any) (int, error) {
	var body io.Reader
	if reqBody != nil {
		data, err := json.Marshal(reqBody)
		if err != nil {
			return 0, fmt.Errorf("resilient: marshal request: %w", err)
		}
		body = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.cfg.baseURL+path, body)
	if err != nil {
		return 0, err
	}
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")

	respData, status, err := c.Do(ctx, req)
	if err != nil {
		return status, err
	}

	if respBody != nil && len(respData) > 0 {
		if err := json.Unmarshal(respData, respBody); err != nil {
			return status, fmt.Errorf("resilient: unmarshal response: %w", err)
		}
	}
	return status, nil
}

// --- internal helpers ---

func (c *Client) waitRateLimit(ctx context.Context) error {
	if c.limiter == nil {
		return nil
	}
	return c.limiter.Wait(ctx)
}

func (c *Client) shouldRetry(attempt int, resp *http.Response, err error) bool {
	if attempt >= c.cfg.maxRetries {
		return false
	}
	if c.cfg.retryPolicy != nil {
		return c.cfg.retryPolicy(attempt, resp, err)
	}
	// Network errors are retryable.
	if err != nil {
		return true
	}
	if resp != nil {
		return c.cfg.retryableStatus[resp.StatusCode]
	}
	return false
}

func (c *Client) backoffDuration(attempt int, lastStatus int) time.Duration {
	base := c.cfg.initialBackoff * time.Duration(1<<(attempt-1))

	// If we have a Retry-After hint, use it as minimum.
	// (We re-check in case caller stored it; for simplicity we use base.)
	_ = lastStatus

	// Add jitter: ±25%
	jitter := float64(base) * 0.25 * (rand.Float64()*2 - 1) //nolint:gosec
	d := time.Duration(float64(base) + jitter)
	if d < 0 {
		d = c.cfg.initialBackoff
	}
	return d
}

// BackoffDuration is exported for testing.
func (c *Client) BackoffDuration(attempt int) time.Duration {
	return c.backoffDuration(attempt, 0)
}

func (c *Client) reduceRateLimit() {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.limiter == nil || c.closed {
		return
	}

	reduced := c.originalRate / 2
	if reduced < 0.01 {
		reduced = 0.01
	}
	c.limiter.SetLimit(reduced)

	if c.adaptiveTimer != nil {
		c.adaptiveTimer.Stop()
	}
	c.adaptiveTimer = time.AfterFunc(c.cfg.adaptiveCooldown, func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		if !c.closed && c.limiter != nil {
			c.limiter.SetLimit(c.originalRate)
		}
	})
}

// parseRetryAfter parses the Retry-After header value.
// It supports both seconds (integer) and HTTP-date formats.
// Returns the duration to wait, or 0 if unparseable.
func parseRetryAfter(val string) time.Duration {
	if val == "" {
		return 0
	}
	val = strings.TrimSpace(val)

	// Try seconds first.
	if secs, err := strconv.ParseFloat(val, 64); err == nil && secs >= 0 {
		return time.Duration(math.Ceil(secs)) * time.Second
	}

	// Try HTTP-date (RFC 1123, RFC 850, ANSI C).
	for _, layout := range []string{
		time.RFC1123,
		time.RFC850,
		"Mon Jan _2 15:04:05 2006",
	} {
		if t, err := time.Parse(layout, val); err == nil {
			d := time.Until(t)
			if d < 0 {
				return 0
			}
			return d
		}
	}
	return 0
}

func applyHeaders(req *http.Request, headers []map[string]string) {
	for _, h := range headers {
		for k, v := range h {
			req.Header.Set(k, v)
		}
	}
}
