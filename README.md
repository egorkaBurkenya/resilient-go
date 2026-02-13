# resilient-go

[![Go Version](https://img.shields.io/github/go-mod/go-version/egorkaBurkenya/resilient-go)](https://github.com/egorkaBurkenya/resilient-go)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Go Reference](https://pkg.go.dev/badge/github.com/egorkaBurkenya/resilient-go.svg)](https://pkg.go.dev/github.com/egorkaBurkenya/resilient-go)

A production-ready Go HTTP client with built-in rate limiting, retry with exponential backoff, and adaptive rate reduction.

## Why Another HTTP Client?

Most Go HTTP libraries force you to choose between simplicity and resilience. You either get a thin wrapper around `net/http` or a heavyweight framework with dozens of dependencies.

**resilient** gives you exactly what you need for API consumption:

- 🪣 **Proactive rate limiting** — token bucket prevents 429s before they happen
- 🔄 **Smart retries** — exponential backoff with jitter, Retry-After header parsing
- 📉 **Adaptive throttling** — automatically halves rate on limit hits, restores after cooldown
- 📊 **Built-in metrics** — atomic counters ready for Prometheus/OpenTelemetry
- 🪶 **Near-zero dependencies** — only `golang.org/x/time/rate` beyond stdlib

## Quick Start

```go
go get github.com/egorkaBurkenya/resilient-go
```

```go
client := resilient.New(
    resilient.WithBaseURL("https://api.example.com"),
    resilient.WithRateLimit(5.0, 2),           // 5 rps, burst 2
    resilient.WithRetry(3, 2*time.Second),     // 3 retries, 2s initial backoff
    resilient.WithAdaptive(5*time.Minute),     // restore rate after 5min
    resilient.WithTimeout(30*time.Second),
)
defer client.Close()

// Simple GET
body, status, err := client.Get(ctx, "/users")

// JSON round-trip
var users []User
status, err := client.DoJSON(ctx, "GET", "/users", nil, &users)

// Standard http.Request
req, _ := http.NewRequestWithContext(ctx, "POST", "https://api.example.com/data", payload)
body, status, err := client.Do(ctx, req)
```

## Features

- ✅ Token bucket rate limiting (golang.org/x/time/rate)
- ✅ Retry with exponential backoff + jitter
- ✅ Configurable retryable status codes (default: 429, 503)
- ✅ Retry-After header parsing (seconds and HTTP-date)
- ✅ Adaptive rate reduction (halve on limit hit, auto-restore)
- ✅ Atomic stats tracking (total, errors, rate-limited)
- ✅ Callbacks: OnError, OnSuccess, OnRateLimited
- ✅ Request/response hooks for logging/metrics
- ✅ Custom retry policy support
- ✅ Context-aware (respects cancellation)
- ✅ Thread-safe for concurrent use
- ✅ Convenience methods: Get, Post, DoJSON
- ✅ Standard Do(ctx, *http.Request) interface
- ✅ Close() for clean resource release
- ✅ Functional options pattern

## Architecture

```
┌───────────────────────────────────────────────┐
│              resilient.Client                 │
├───────────────────────────────────────────────┤
│                                               │
│  Get/Post/DoJSON                              │
│       │                                       │
│       ▼                                       │
│  Request Hook ──► Rate Limiter (token bucket) │
│       │                                       │
│       ▼                                       │
│  ┌─────────────────────────────────────────┐  │
│  │            Retry Loop                   │  │
│  │                                         │  │
│  │  ┌───────────────┐                      │  │
│  │  │ http.Client.Do │                      │  │
│  │  └───────┬───────┘                      │  │
│  │          ▼                              │  │
│  │    Response Hook                        │  │
│  │          │                              │  │
│  │    429/503? ──► backoff + jitter        │  │
│  │                 + adaptive reduction    │  │
│  └─────────────────────────────────────────┘  │
│                                               │
│  Stats ◄── atomic counters                    │
│  Callbacks ◄── OnError / OnSuccess            │
└───────────────────────────────────────────────┘
```

## Comparison

| Feature | resilient | go-retryablehttp | resty |
|---|---|---|---|
| Rate limiting | ✅ Token bucket | ❌ | ❌ |
| Adaptive throttling | ✅ | ❌ | ❌ |
| Retry-After parsing | ✅ | ✅ | ❌ |
| Jitter on backoff | ✅ | ✅ | ❌ |
| Built-in metrics | ✅ Atomic | ❌ | ❌ |
| Dependencies | 1 (x/time) | 2 | 10+ |
| Functional options | ✅ | ❌ (struct) | ❌ (builder) |
| JSON helpers | ✅ | ❌ | ✅ |

## Configuration

All options have sensible defaults. Create a zero-config client with just `resilient.New()`.

| Option | Default | Description |
|---|---|---|
| `WithBaseURL` | `""` | Base URL for convenience methods |
| `WithRateLimit` | disabled | Token bucket: rps + burst |
| `WithRetry` | 3 retries, 2s | Max retries + initial backoff |
| `WithAdaptive` | 5 min | Cooldown before rate restore |
| `WithTimeout` | 30s | HTTP client timeout |
| `WithMaxResponseSize` | 10 MB | Response body size limit |
| `WithRetryableStatus` | 429, 503 | Status codes that trigger retry |
| `WithRetryPolicy` | nil | Custom retry decision function |
| `WithHTTPClient` | nil | Custom underlying http.Client |

## Performance

- Rate limiter: O(1) per request (token bucket)
- Stats: lock-free atomic counters
- No allocations in hot path beyond stdlib HTTP
- Body buffered once for retries (unavoidable)

## License

[MIT](LICENSE)
