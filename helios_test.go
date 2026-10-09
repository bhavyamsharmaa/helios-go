package helios

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func newTestClient(t *testing.T, url string, opts ...Option) *Client {
	t.Helper()
	opts = append([]Option{WithBaseURL(url)}, opts...)
	c, err := New("test-key", opts...)
	if err != nil {
		t.Fatal(err)
	}
	c.retryBase = time.Millisecond
	c.reconnectBase = 5 * time.Millisecond
	c.reconnectMax = 20 * time.Millisecond
	return c
}

func TestNewValidation(t *testing.T) {
	t.Setenv(EnvBaseURL, "")
	if _, err := New(""); !errors.Is(err, ErrMissingSDKKey) {
		t.Errorf("empty key: got %v", err)
	}
	if _, err := New("k"); !errors.Is(err, ErrMissingBaseURL) {
		t.Errorf("no base URL: got %v", err)
	}
	if _, err := New("k", WithBaseURL("not a url")); !errors.Is(err, ErrInvalidBaseURL) {
		t.Errorf("bad base URL: got %v", err)
	}
	t.Setenv(EnvBaseURL, "https://example.com/")
	c, err := New("k")
	if err != nil {
		t.Fatal(err)
	}
	if c.baseURL != "https://example.com" {
		t.Errorf("trailing slash not trimmed: %q", c.baseURL)
	}
}

func TestEvaluateSendsKeyAndBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/evaluate" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("X-Helios-SDK-Key"); got != "test-key" {
			t.Errorf("SDK key header = %q", got)
		}
		if got := r.Header.Get("User-Agent"); got != "helios-go/"+Version {
			t.Errorf("User-Agent = %q", got)
		}
		var body struct {
			Context struct {
				SubjectKey string         `json:"subjectKey"`
				Attributes map[string]any `json:"attributes"`
			} `json:"context"`
			FlagKeys []string `json:"flagKeys"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if len(body.FlagKeys) != 1 || body.FlagKeys[0] != "new-checkout" ||
			body.Context.SubjectKey != "user-1" || body.Context.Attributes["plan"] != "pro" {
			t.Errorf("unexpected body: %+v", body)
		}
		_, _ = w.Write([]byte(`{"evaluations":[{"flagKey":"new-checkout","value":true,"variationId":"v1","reason":"ROLLOUT","experimentId":"exp-1"}]}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	ev, err := c.Evaluate(context.Background(), "new-checkout", Subject{Key: "user-1", Attributes: map[string]any{"plan": "pro"}})
	if err != nil {
		t.Fatal(err)
	}
	if ev.Value != true || ev.VariationID != "v1" || ev.ExperimentID != "exp-1" {
		t.Errorf("unexpected evaluation: %+v", ev)
	}
}

func TestEvaluateRequiresKeys(t *testing.T) {
	c := newTestClient(t, "http://localhost:1")
	if _, err := c.Evaluate(context.Background(), "", Subject{Key: "u"}); !errors.Is(err, ErrMissingFlagKey) {
		t.Errorf("got %v", err)
	}
	if _, err := c.Evaluate(context.Background(), "f", Subject{}); !errors.Is(err, ErrMissingSubjectKey) {
		t.Errorf("got %v", err)
	}
}

func TestTypedVariations(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			FlagKeys []string `json:"flagKeys"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		key := body.FlagKeys[0]
		values := map[string]string{
			"bool":     `{"value":true,"reason":"ROLLOUT"}`,
			"string":   `{"value":"blue","reason":"ROLLOUT"}`,
			"int":      `{"value":42,"reason":"ROLLOUT"}`,
			"frac":     `{"value":2.5,"reason":"ROLLOUT"}`,
			"missing":  `{"value":null,"variationId":null,"reason":"FLAG_NOT_FOUND"}`,
			"disabled": `{"value":null,"variationId":null,"reason":"FLAG_DISABLED"}`,
		}
		item := strings.Replace(values[key], "{", `{"flagKey":"`+key+`",`, 1)
		_, _ = w.Write([]byte(`{"evaluations":[` + item + `]}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	ctx := context.Background()
	s := Subject{Key: "u"}

	if !c.BoolVariation(ctx, "bool", s, false) {
		t.Error("BoolVariation")
	}
	if got := c.StringVariation(ctx, "string", s, "red"); got != "blue" {
		t.Errorf("StringVariation = %q", got)
	}
	if got := c.IntVariation(ctx, "int", s, 0); got != 42 {
		t.Errorf("IntVariation = %d", got)
	}
	if got := c.IntVariation(ctx, "frac", s, 7); got != 7 {
		t.Errorf("IntVariation on 2.5 should return default, got %d", got)
	}
	if got := c.Float64Variation(ctx, "frac", s, 0); got != 2.5 {
		t.Errorf("Float64Variation = %v", got)
	}
	// wrong type -> default
	if got := c.BoolVariation(ctx, "string", s, true); !got {
		t.Error("type mismatch should return default")
	}
	// disabled flag -> default
	if got := c.BoolVariation(ctx, "disabled", s, true); !got {
		t.Error("disabled flag should return default")
	}
	// unknown flag -> default
	if got := c.StringVariation(ctx, "missing", s, "fallback"); got != "fallback" {
		t.Errorf("missing flag = %q", got)
	}
}

func TestDefaultWhenServerDown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close() // nothing listens any more

	c := newTestClient(t, url, WithMaxRetries(1))
	if got := c.BoolVariation(context.Background(), "f", Subject{Key: "u"}, true); !got {
		t.Error("should fall back to default when Helios is unreachable")
	}
}

func TestRetriesOn5xxThenSucceeds(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) < 3 {
			http.Error(w, `{"code":"BOOM","message":"boom"}`, http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte(`{"evaluations":[{"flagKey":"f","value":true,"reason":"ROLLOUT"}]}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL, WithMaxRetries(2))
	if !c.BoolVariation(context.Background(), "f", Subject{Key: "u"}, false) {
		t.Error("expected success after retries")
	}
	if n := atomic.LoadInt32(&calls); n != 3 {
		t.Errorf("calls = %d, want 3", n)
	}
}

func TestNoRetryOn4xx(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		http.Error(w, `{"code":"UNAUTHORIZED","message":"bad key"}`, http.StatusUnauthorized)
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL, WithMaxRetries(3))
	_, err := c.Evaluate(context.Background(), "f", Subject{Key: "u"})
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("err = %v, want ErrUnauthorized", err)
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != 401 || apiErr.Message != "bad key" {
		t.Errorf("unexpected APIError: %+v", apiErr)
	}
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Errorf("calls = %d, want 1 (no retry on 401)", n)
	}
}

func TestEvaluateRespectsTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body) // lets the server notice the client hanging up
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL, WithTimeout(30*time.Millisecond), WithMaxRetries(0))
	start := time.Now()
	if _, err := c.Evaluate(context.Background(), "f", Subject{Key: "u"}); err == nil {
		t.Fatal("expected timeout error")
	}
	if time.Since(start) > time.Second {
		t.Error("timeout was not enforced")
	}
}

func TestSubscribeReceivesUpdatesAndReconnects(t *testing.T) {
	var connects int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/sdk/stream" || r.Header.Get("X-Helios-SDK-Key") != "test-key" {
			http.Error(w, "nope", http.StatusUnauthorized)
			return
		}
		n := atomic.AddInt32(&connects, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		f := w.(http.Flusher)
		_, _ = w.Write([]byte(": heartbeat\n\n"))
		if n == 1 {
			_, _ = w.Write([]byte("event: flag_update\ndata: {\"action\":\"update\",\"flagKey\":\"a\",\"enabled\":false,\"version\":7}\n\n"))
			f.Flush()
			return // close -> client must reconnect
		}
		_, _ = w.Write([]byte("event: other\ndata: {}\n\n"))
		_, _ = w.Write([]byte("event: flag_update\ndata: {\"action\":\"toggle\",\"flagKey\":\"b\",\"enabled\":true,\"version\":8}\n\n"))
		f.Flush()
		<-r.Context().Done()
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	updates := make(chan FlagUpdate, 4)
	done := make(chan error, 1)
	go func() { done <- c.Subscribe(ctx, func(u FlagUpdate) { updates <- u }) }()

	first := waitUpdate(t, updates)
	if first.FlagKey != "a" || first.Enabled || first.Version != 7 || first.Action != "update" {
		t.Errorf("first update = %+v", first)
	}
	second := waitUpdate(t, updates)
	if second.FlagKey != "b" || !second.Enabled || second.Version != 8 {
		t.Errorf("second update = %+v", second)
	}
	if n := atomic.LoadInt32(&connects); n < 2 {
		t.Errorf("connects = %d, expected a reconnect", n)
	}

	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Subscribe returned %v", err)
	}
}

func TestSubscribeStopsOnBadKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"code":"UNAUTHORIZED","message":"revoked"}`, http.StatusUnauthorized)
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := c.Subscribe(ctx, func(FlagUpdate) {})
	if !errors.Is(err, ErrUnauthorized) {
		t.Errorf("err = %v, want ErrUnauthorized", err)
	}
}

func waitUpdate(t *testing.T, ch <-chan FlagUpdate) FlagUpdate {
	t.Helper()
	select {
	case u := <-ch:
		return u
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for flag update")
		return FlagUpdate{}
	}
}

func TestSubscribeParsesMultiLineData(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(": ping\r\n\r\nevent: flag_update\r\ndata: {\"action\":\"toggle\",\r\ndata: \"flagKey\":\"kill\",\"enabled\":false,\"version\":3}\r\n\r\n"))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	updates := make(chan FlagUpdate, 1)
	go func() { _ = c.Subscribe(ctx, func(u FlagUpdate) { updates <- u }) }()

	if u := waitUpdate(t, updates); u.FlagKey != "kill" || u.Enabled || u.Version != 3 {
		t.Errorf("update = %+v", u)
	}
}

func TestSubscribeStopsOnForbidden(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		http.Error(w, "no", http.StatusForbidden)
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	if err := c.Subscribe(context.Background(), func(FlagUpdate) {}); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("err = %v", err)
	}
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Errorf("calls = %d, want 1", n)
	}
}

func TestRetriesOn429(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			http.Error(w, "slow down", http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(`{"evaluations":[{"flagKey":"f","value":"x","reason":"ROLLOUT"}]}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	if got := c.StringVariation(context.Background(), "f", Subject{Key: "u"}, "d"); got != "x" {
		t.Errorf("got %q", got)
	}
}

func TestResponseSizeCapped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"evaluations":[{"flagKey":"f","value":"`))
		_, _ = w.Write([]byte(strings.Repeat("a", 2*maxResponseBytes)))
		_, _ = w.Write([]byte(`"}]}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL, WithMaxRetries(0))
	if _, err := c.Evaluate(context.Background(), "f", Subject{Key: "u"}); err == nil {
		t.Error("oversized response should be rejected")
	}
}

func TestEvaluateRespectsContextCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		<-r.Context().Done()
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL, WithTimeout(0))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := c.Evaluate(ctx, "f", Subject{Key: "u"}); err == nil {
		t.Error("expected error after cancel")
	}
}

type captureHandler struct{ sb *strings.Builder }

func (h captureHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h captureHandler) Handle(_ context.Context, r slog.Record) error {
	h.sb.WriteString(r.Message)
	r.Attrs(func(a slog.Attr) bool { h.sb.WriteString(" " + a.Key + "=" + a.Value.String()); return true })
	return nil
}
func (h captureHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h captureHandler) WithGroup(string) slog.Handler      { return h }

func TestSDKKeyNeverLogged(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad key: "+r.Header.Get("X-Helios-SDK-Key")[:0], http.StatusUnauthorized)
	}))
	defer srv.Close()

	var sb strings.Builder
	c := newTestClient(t, srv.URL, WithLogger(slog.New(captureHandler{&sb})))
	c.BoolVariation(context.Background(), "f", Subject{Key: "u"}, false)
	if sb.Len() == 0 {
		t.Fatal("expected a log line")
	}
	if strings.Contains(sb.String(), "test-key") {
		t.Errorf("SDK key leaked into logs: %s", sb.String())
	}
}
