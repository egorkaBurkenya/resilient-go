package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/egorkaBurkenya/resilient-go"
)

func main() {
	// Each API gets its own independent client with tailored settings.
	github := resilient.New(
		resilient.WithBaseURL("https://api.github.com"),
		resilient.WithRateLimit(10, 3),
		resilient.WithRetry(3, 1*time.Second),
		resilient.WithAdaptive(5*time.Minute),
	)
	defer github.Close()

	jsonplaceholder := resilient.New(
		resilient.WithBaseURL("https://jsonplaceholder.typicode.com"),
		resilient.WithRateLimit(20, 5),
		resilient.WithRetry(2, 500*time.Millisecond),
	)
	defer jsonplaceholder.Close()

	ctx := context.Background()

	body, _, err := github.Get(ctx, "/zen")
	if err != nil {
		log.Printf("GitHub error: %v", err)
	} else {
		fmt.Printf("GitHub Zen: %s\n", body)
	}

	body, _, err = jsonplaceholder.Get(ctx, "/todos/1")
	if err != nil {
		log.Printf("JSONPlaceholder error: %v", err)
	} else {
		fmt.Printf("Todo: %s\n", body[:80])
	}

	fmt.Printf("GitHub stats: %+v\n", github.Stats())
	fmt.Printf("JSONPlaceholder stats: %+v\n", jsonplaceholder.Stats())
}
