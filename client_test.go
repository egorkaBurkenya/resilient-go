package resilient

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestBasicGet(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	c := New(WithBaseURL(srv.URL), WithTimeout(5*time.Second))
	defer c.Close()

	body, status, err := c.Get(context.Background(), "/test")
	if err != nil {
		t.Fatal(err)
	}
	if status != 200 {
		t.Fatalf("expected 200, got %d", status)
	}
	if string(body) != `{"ok":true}` {
		t.Fatalf("unexpected body: %s", body)
	}
}

func TestRetryOn429(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := attempts.Add(1)
		if n <= 2 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(429)
			w.Write([]byte("rate limited"))
			return
		}
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	c := New(
		WithBaseURL(srv.URL),
		WithRetry(3, 10*time.Millisecond),
		WithTimeout(5*time.Second),
	)
	defer c.Close()

	body, status, err := c.Get(context.Background(), "/")
	if err != nil {
		t.Fatal(err)
	}
	if status != 200 {
		t.Fatalf("expected 200, got %d", status)
	}
	if string(body) != "ok" {
		t.Fatalf("unexpected body: %s", body)
	}
	if n := attempts.Load(); n != 3 {
		t.Fatalf("expected 3 attempts, got %d", n)
	}
}

func TestRetryOn503(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := attempts.Add(1)
		if n == 1 {
			w.WriteHeader(503)
			return
		}
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	c := New(WithBaseURL(srv.URL), WithRetry(2, 10*time.Millisecond))
	defer c.Close()

	_, status, err := c.Get(context.Background(), "/")
	if err != nil {
		t.Fatal(err)
	}
	if status != 200 {
		t.Fatalf("expected 200, got %d", status)
	}
}

func TestMaxRetriesExhausted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(429)
	}))
	defer srv.Close()

	c := New(WithBaseURL(srv.URL), WithRetry(2, 10*time.Millisecond))
	defer c.Close()

	_, status, err := c.Get(context.Background(), "/")
	if err == nil {
		t.Fatal("expected error")
	}
	if status != 429 {
		t.Fatalf("expected status 429, got %d", status)
	}
}

func TestContextCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()

	c := New(WithBaseURL(srv.URL), WithTimeout(10*time.Second))
	defer c.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, _, err := c.Get(ctx, "/")
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
}

func TestRateLimiting(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	// 10 rps, burst 1 — so 3 requests should take ~200ms
	c := New(WithBaseURL(srv.URL), WithRateLimit(10, 1))
	defer c.Close()

	start := time.Now()
	for i := 0; i < 3; i++ {
		_, _, err := c.Get(context.Background(), "/")
		if err != nil {
			t.Fatal(err)
		}
	}
	elapsed := time.Since(start)

	// Should take at least 190ms (2 waits × 100ms each, minus some tolerance)
	if elapsed < 150*time.Millisecond {
		t.Fatalf("rate limiting too fast: %v", elapsed)
	}
}

func TestAdaptiveRateReduction(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := attempts.Add(1)
		if n == 1 {
			w.WriteHeader(429)
			return
		}
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	c := New(
		WithBaseURL(srv.URL),
		WithRateLimit(100, 10),
		WithRetry(2, 10*time.Millisecond),
		WithAdaptive(500*time.Millisecond),
	)
	defer c.Close()

	_, _, err := c.Get(context.Background(), "/")
	if err != nil {
		t.Fatal(err)
	}

	// Rate should be halved.
	c.mu.Lock()
	currentRate := c.limiter.Limit()
	c.mu.Unlock()
	if currentRate >= 100 {
		t.Fatalf("expected reduced rate, got %v", currentRate)
	}

	// Wait for adaptive restore.
	time.Sleep(700 * time.Millisecond)

	c.mu.Lock()
	restoredRate := c.limiter.Limit()
	c.mu.Unlock()
	if restoredRate != 100 {
		t.Fatalf("expected restored rate 100, got %v", restoredRate)
	}
}

func TestStatsAccuracy(t *testing.T) {
	var count atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := count.Add(1)
		if n%3 == 0 {
			w.WriteHeader(429)
			return
		}
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	c := New(WithBaseURL(srv.URL), WithRetry(0, 10*time.Millisecond))
	defer c.Close()

	successes := 0
	for i := 0; i < 6; i++ {
		_, _, err := c.Get(context.Background(), "/")
		if err == nil {
			successes++
		}
	}

	s := c.Stats()
	if s.TotalRequests != 6 {
		t.Fatalf("expected 6 total, got %d", s.TotalRequests)
	}
	if s.RateLimited != 2 {
		t.Fatalf("expected 2 rate limited, got %d", s.RateLimited)
	}
}

func TestConcurrentSafety(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	c := New(WithBaseURL(srv.URL), WithRateLimit(100, 10))
	defer c.Close()

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, err := c.Get(context.Background(), "/")
			if err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()

	s := c.Stats()
	if s.TotalRequests != 20 {
		t.Fatalf("expected 20 total, got %d", s.TotalRequests)
	}
}

func TestDoJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in map[string]string
		json.NewDecoder(r.Body).Decode(&in)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"echo": in["msg"]})
	}))
	defer srv.Close()

	c := New(WithBaseURL(srv.URL))
	defer c.Close()

	var resp map[string]string
	status, err := c.DoJSON(context.Background(), http.MethodPost, "/", map[string]string{"msg": "hello"}, &resp)
	if err != nil {
		t.Fatal(err)
	}
	if status != 200 {
		t.Fatalf("expected 200, got %d", status)
	}
	if resp["echo"] != "hello" {
		t.Fatalf("unexpected response: %v", resp)
	}
}

func TestRetryAfterHeaderParsing(t *testing.T) {
	tests := []struct {
		val      string
		expected time.Duration
	}{
		{"5", 5 * time.Second},
		{"0", 0},
		{"1.5", 2 * time.Second}, // ceil
		{"", 0},
		{"garbage", 0},
	}
	for _, tt := range tests {
		got := parseRetryAfter(tt.val)
		if got != tt.expected {
			t.Errorf("parseRetryAfter(%q) = %v, want %v", tt.val, got, tt.expected)
		}
	}
}

func TestJitterRange(t *testing.T) {
	c := New(WithRetry(5, 100*time.Millisecond))
	defer c.Close()

	for i := 0; i < 100; i++ {
		d := c.BackoffDuration(1) // attempt 1 → base = 100ms
		// Expect within ±25%: 75ms to 125ms
		if d < 75*time.Millisecond || d > 125*time.Millisecond {
			t.Fatalf("jitter out of range: %v", d)
		}
	}
}

func TestCallbacks(t *testing.T) {
	var (
		errorCalled      atomic.Int32
		successCalled    atomic.Int32
		rateLimitCalled  atomic.Int32
	)

	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := attempts.Add(1)
		if n == 1 {
			w.WriteHeader(429)
			return
		}
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	c := New(
		WithBaseURL(srv.URL),
		WithRetry(2, 10*time.Millisecond),
		WithOnError(func(code int, req *http.Request) { errorCalled.Add(1) }),
		WithOnSuccess(func(req *http.Request, resp *http.Response) { successCalled.Add(1) }),
		WithOnRateLimited(func(req *http.Request) { rateLimitCalled.Add(1) }),
	)
	defer c.Close()

	_, _, err := c.Get(context.Background(), "/")
	if err != nil {
		t.Fatal(err)
	}

	if errorCalled.Load() != 1 {
		t.Fatalf("expected 1 error callback, got %d", errorCalled.Load())
	}
	if successCalled.Load() != 1 {
		t.Fatalf("expected 1 success callback, got %d", successCalled.Load())
	}
	if rateLimitCalled.Load() != 1 {
		t.Fatalf("expected 1 rate limit callback, got %d", rateLimitCalled.Load())
	}
}

func TestCustomRetryPolicy(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := attempts.Add(1)
		if n <= 2 {
			w.WriteHeader(500)
			return
		}
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	c := New(
		WithBaseURL(srv.URL),
		WithRetry(5, 10*time.Millisecond),
		WithRetryPolicy(func(attempt int, resp *http.Response, err error) bool {
			if resp != nil && resp.StatusCode == 500 {
				return true
			}
			return false
		}),
	)
	defer c.Close()

	body, status, err := c.Get(context.Background(), "/")
	if err != nil {
		t.Fatal(err)
	}
	if status != 200 || string(body) != "ok" {
		t.Fatalf("unexpected: status=%d body=%s", status, body)
	}
}

func TestRetryableStatusCodes(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := attempts.Add(1)
		if n == 1 {
			w.WriteHeader(502)
			return
		}
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	c := New(
		WithBaseURL(srv.URL),
		WithRetry(2, 10*time.Millisecond),
		WithRetryableStatus(502),
	)
	defer c.Close()

	_, status, err := c.Get(context.Background(), "/")
	if err != nil {
		t.Fatal(err)
	}
	if status != 200 {
		t.Fatalf("expected 200, got %d", status)
	}
}

func TestRequestResponseHooks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Hook") != "applied" {
			w.WriteHeader(400)
			return
		}
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	var responseHookCalled atomic.Int32
	c := New(
		WithBaseURL(srv.URL),
		WithRequestHook(func(req *http.Request) {
			req.Header.Set("X-Hook", "applied")
		}),
		WithResponseHook(func(resp *http.Response) {
			responseHookCalled.Add(1)
		}),
	)
	defer c.Close()

	_, status, err := c.Get(context.Background(), "/")
	if err != nil {
		t.Fatal(err)
	}
	if status != 200 {
		t.Fatalf("expected 200, got %d", status)
	}
	if responseHookCalled.Load() != 1 {
		t.Fatal("response hook not called")
	}
}

func TestNoRateLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	c := New(WithBaseURL(srv.URL))
	defer c.Close()

	for i := 0; i < 10; i++ {
		_, _, err := c.Get(context.Background(), "/")
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestPost(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(405)
			return
		}
		body, _ := io.ReadAll(r.Body)
		w.Write(body)
	}))
	defer srv.Close()

	c := New(WithBaseURL(srv.URL))
	defer c.Close()

	body, status, err := c.Post(context.Background(), "/", "text/plain", strings.NewReader("hello"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if status != 200 || string(body) != "hello" {
		t.Fatalf("unexpected: status=%d body=%s", status, body)
	}
}

func TestSetRateLimit(t *testing.T) {
	c := New(WithRateLimit(1, 1))
	defer c.Close()

	c.SetRateLimit(100, 10)

	c.mu.Lock()
	r := c.limiter.Limit()
	c.mu.Unlock()

	if r != 100 {
		t.Fatalf("expected rate 100, got %v", r)
	}
}

// Ensure the package imports are used.
var _ = fmt.Sprintf
