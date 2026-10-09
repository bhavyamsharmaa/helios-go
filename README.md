# helios-go

[![Go Reference](https://pkg.go.dev/badge/github.com/bhavyamsharmaa/helios-go.svg)](https://pkg.go.dev/github.com/bhavyamsharmaa/helios-go)
[![CI](https://github.com/bhavyamsharmaa/helios-go/actions/workflows/ci.yml/badge.svg)](https://github.com/bhavyamsharmaa/helios-go/actions/workflows/ci.yml)

Go client for **Helios**, a feature-flag and A/B-testing platform. Create flags
and rollouts in the Helios console, then read them from your Go backend.

> **Status: v0.** The API may change between minor versions until v1.0.0.

## Install

```sh
go get github.com/bhavyamsharmaa/helios-go
```

Requires Go 1.21 or newer.

## Quickstart

```go
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	helios "github.com/bhavyamsharmaa/helios-go"
)

func main() {
	client, err := helios.New(os.Getenv("HELIOS_SDK_KEY"),
		helios.WithBaseURL("https://your-helios-host"))
	if err != nil {
		log.Fatal(err)
	}

	user := helios.Subject{
		Key:        "user-123", // stable ID: same user, same variation
		Attributes: map[string]any{"country": "IN", "plan": "pro"},
	}

	// The last argument is the default, used if Helios is unreachable.
	if client.BoolVariation(context.Background(), "new-checkout", user, false) {
		fmt.Println("new checkout")
	} else {
		fmt.Println("old checkout")
	}
}
```

Get an SDK key from your Helios console and keep it in an environment variable
or secret manager. SDK keys are secrets: never commit them or ship them in
client-side code.

## Reading flags

| Method | Returns |
|---|---|
| `BoolVariation(ctx, flag, subject, def)` | `bool` |
| `StringVariation(ctx, flag, subject, def)` | `string` |
| `IntVariation(ctx, flag, subject, def)` | `int` (whole numbers only) |
| `Float64Variation(ctx, flag, subject, def)` | `float64` |
| `Evaluate(ctx, flag, subject)` | `*Evaluation` with `FlagKey`, `Value`, `VariationID`, `Reason`, `ExperimentID`, plus an `error` |

The `*Variation` helpers **never return an error**. They return your default
when Helios is unreachable, the flag does not exist (`FLAG_NOT_FOUND`) or is disabled (`FLAG_DISABLED`), the SDK
key is rejected, or the value has a different type than you asked for. Your app
keeps working if Helios goes down. Use `Evaluate` when you want to see errors
or the evaluation reason.

## Reacting to changes (kill switch)

```go
go func() {
	err := client.Subscribe(ctx, func(u helios.FlagUpdate) {
		log.Printf("flag %s changed: enabled=%v version=%d", u.FlagKey, u.Enabled, u.Version)
	})
	log.Println("stream stopped:", err)
}()
```

`Subscribe` holds a Server-Sent Events connection to Helios, reconnects with
exponential backoff when it drops, and returns when the context is cancelled or
the SDK key is rejected.

## Options

| Option | Default | Purpose |
|---|---|---|
| `WithBaseURL(url)` | `$HELIOS_BASE_URL` | Helios API address |
| `WithTimeout(d)` | 3s | Per-attempt timeout for `Evaluate` |
| `WithMaxRetries(n)` | 2 | Retries on network errors, 429 and 5xx |
| `WithHTTPClient(hc)` | `&http.Client{}` | Custom transport or proxy. Do not set `Timeout` on it; use `WithTimeout`. |
| `WithLogger(l)` | silent | `*slog.Logger` for diagnostics (never logs the key) |

## Errors

```go
_, err := client.Evaluate(ctx, "flag", subject)
switch {
case errors.Is(err, helios.ErrUnauthorized): // bad or revoked SDK key
	// ...
}

var apiErr *helios.APIError
if errors.As(err, &apiErr) {
	log.Println(apiErr.StatusCode, apiErr.Message)
}
```

## Other languages

This module is for Go backends. Helios exposes a plain REST + SSE API, so other
languages can call it directly (`POST /evaluate` with `{"context": {"subjectKey": ...}, "flagKeys": [...]}`,
`GET /sdk/stream`, header `X-Helios-SDK-Key`). See `api/openapi.yaml` in the Helios repo.

## Contributing

Issues and pull requests are welcome. Run `go vet ./... && go test -race ./...`
before opening a PR.

## License

[MIT](LICENSE)
