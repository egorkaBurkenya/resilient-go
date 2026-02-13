package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/egorkaBurkenya/resilient-go"
)

func main() {
	client := resilient.New(
		resilient.WithBaseURL("https://httpbin.org"),
		resilient.WithRateLimit(2.0, 1),
		resilient.WithRetry(3, 1*time.Second),
		resilient.WithAdaptive(30*time.Second),
		resilient.WithOnRateLimited(func(req *http.Request) {
			fmt.Printf("⚠️  Rate limited on %s\n", req.URL.Path)
		}),
		resilient.WithOnSuccess(func(req *http.Request, resp *http.Response) {
			fmt.Printf("✅ %s %s → %d\n", req.Method, req.URL.Path, resp.StatusCode)
		}),
		resilient.WithOnError(func(code int, req *http.Request) {
			fmt.Printf("❌ %s %s → %d\n", req.Method, req.URL.Path, code)
		}),
	)
	defer client.Close()

	ctx := context.Background()

	// Make several requests to demonstrate adaptive behavior.
	for i := 0; i < 5; i++ {
		_, status, err := client.Get(ctx, fmt.Sprintf("/get?i=%d", i))
		if err != nil {
			log.Printf("Request %d: error: %v", i, err)
		} else {
			log.Printf("Request %d: status %d", i, status)
		}
	}

	stats := client.Stats()
	fmt.Printf("\nFinal stats: %d total, %d errors, %d rate-limited\n",
		stats.TotalRequests, stats.TotalErrors, stats.RateLimited)
}
