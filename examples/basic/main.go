package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/egorkaBurkenya/resilient-go"
)

func main() {
	client := resilient.New(
		resilient.WithBaseURL("https://httpbin.org"),
		resilient.WithRateLimit(5.0, 2),
		resilient.WithRetry(3, 2*time.Second),
		resilient.WithTimeout(30*time.Second),
	)
	defer client.Close()

	body, status, err := client.Get(context.Background(), "/get")
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("Status: %d\nBody: %s\n", status, body[:100])

	stats := client.Stats()
	fmt.Printf("Stats: %d total, %d errors, %d rate-limited\n",
		stats.TotalRequests, stats.TotalErrors, stats.RateLimited)
}
