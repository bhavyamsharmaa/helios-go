// Package helios is a small client for the Helios feature-flag and A/B-testing
// platform.
//
// Flags are evaluated server-side by the hosted Helios API, so this client is
// deliberately thin: it sends a flag key and a subject and gets back the value
// Helios chose. Every typed helper (BoolVariation, StringVariation, ...) takes
// a default and returns it whenever Helios is unreachable, the flag does not
// exist, or the value has an unexpected type, so your application keeps
// working even if Helios is down.
//
//	client, err := helios.New(os.Getenv("HELIOS_SDK_KEY"),
//		helios.WithBaseURL("https://your-helios-host"))
//	if err != nil {
//		log.Fatal(err)
//	}
//
//	user := helios.Subject{Key: "user-123"}
//	if client.BoolVariation(ctx, "new-checkout", user, false) {
//		// show the new checkout
//	}
//
// SDK keys are secrets meant for servers. Do not ship them in client-side code.
package helios

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	// Version is the SDK version, sent in the User-Agent header.
	Version = "0.1.0"

	// EnvBaseURL is the environment variable used when WithBaseURL is not set.
	EnvBaseURL = "HELIOS_BASE_URL"

	sdkKeyHeader     = "X-Helios-SDK-Key"
	maxResponseBytes = 1 << 20 // 1 MiB cap on evaluate responses
)

// Client talks to the Helios API. It is safe for concurrent use by multiple
// goroutines; create one and share it.
type Client struct {
	sdkKey         string
	baseURL        string
	httpClient     *http.Client
	requestTimeout time.Duration
	maxRetries     int
	logger         *slog.Logger

	// Backoff tuning. Unexported so tests can shrink them.
	retryBase     time.Duration
	reconnectBase time.Duration
	reconnectMax  time.Duration
}

// Option configures a Client.
type Option func(*Client)

// WithBaseURL sets the Helios API base URL, for example
// "https://helios.example.com". If omitted, the HELIOS_BASE_URL environment
// variable is used.
func WithBaseURL(rawURL string) Option {
	return func(c *Client) { c.baseURL = rawURL }
}

// WithHTTPClient sets the HTTP client used for all requests.
//
// Do not set http.Client.Timeout on it: that timeout also covers reading the
// response body and would cut the Subscribe stream off. Use WithTimeout to
// bound individual Evaluate calls instead.
func WithHTTPClient(hc *http.Client) Option {
	return func(c *Client) {
		if hc != nil {
			c.httpClient = hc
		}
	}
}

// WithTimeout sets the per-attempt timeout for Evaluate calls. The default is
// 3 seconds. A value of zero or less disables the per-attempt timeout (the
// caller's context still applies).
func WithTimeout(d time.Duration) Option {
	return func(c *Client) { c.requestTimeout = d }
}

// WithMaxRetries sets how many times Evaluate retries after a network error,
// HTTP 429 or HTTP 5xx. The default is 2. Use 0 to disable retries.
func WithMaxRetries(n int) Option {
	return func(c *Client) {
		if n < 0 {
			n = 0
		}
		c.maxRetries = n
	}
}

// WithLogger sets the logger for diagnostics such as stream reconnects. By
// default the SDK logs nothing. The SDK key is never logged.
func WithLogger(l *slog.Logger) Option {
	return func(c *Client) {
		if l != nil {
			c.logger = l
		}
	}
}

// New creates a Client. sdkKey is required; the base URL must come from
// WithBaseURL or the HELIOS_BASE_URL environment variable.
func New(sdkKey string, opts ...Option) (*Client, error) {
	if strings.TrimSpace(sdkKey) == "" {
		return nil, ErrMissingSDKKey
	}

	c := &Client{
		sdkKey:         sdkKey,
		requestTimeout: 3 * time.Second,
		maxRetries:     2,
		logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		retryBase:      100 * time.Millisecond,
		reconnectBase:  time.Second,
		reconnectMax:   30 * time.Second,
	}
	for _, opt := range opts {
		opt(c)
	}

	if c.baseURL == "" {
		c.baseURL = os.Getenv(EnvBaseURL)
	}
	if c.baseURL == "" {
		return nil, ErrMissingBaseURL
	}
	u, err := url.Parse(c.baseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("%w: %q", ErrInvalidBaseURL, c.baseURL)
	}
	c.baseURL = strings.TrimRight(u.String(), "/")

	if c.httpClient == nil {
		c.httpClient = &http.Client{}
	}
	return c, nil
}

// newRequest builds a request carrying the SDK key and User-Agent.
func (c *Client) newRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set(sdkKeyHeader, c.sdkKey)
	req.Header.Set("User-Agent", "helios-go/"+Version)
	return req, nil
}

// sleepCtx waits for d or until ctx is done, whichever comes first.
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
