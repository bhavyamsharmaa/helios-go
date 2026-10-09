package helios

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// FlagUpdate is pushed by Helios when a flag changes, for example when someone
// flips the kill switch in the console.
type FlagUpdate struct {
	Action  string `json:"action"`
	FlagKey string `json:"flagKey"`
	Enabled bool   `json:"enabled"`
	Version int64  `json:"version"`
}

// Subscribe listens to the Helios update stream and calls handler for every
// "flag_update" event. It blocks until ctx is cancelled, so run it in its own
// goroutine:
//
//	go func() {
//		err := client.Subscribe(ctx, func(u helios.FlagUpdate) {
//			cache.Invalidate(u.FlagKey)
//		})
//		log.Println("helios stream stopped:", err)
//	}()
//
// Dropped connections are re-established automatically with exponential
// backoff. Subscribe returns early only when ctx is done (the error is
// ctx.Err()) or when the SDK key is rejected (the error matches
// ErrUnauthorized), since retrying cannot fix a bad key.
//
// handler runs on the stream goroutine, so keep it fast and do not block it.
func (c *Client) Subscribe(ctx context.Context, handler func(FlagUpdate)) error {
	if handler == nil {
		return errors.New("helios: handler is required")
	}

	delay := c.reconnectBase
	for {
		connected, err := c.streamOnce(ctx, handler)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if errors.Is(err, ErrUnauthorized) {
			return err
		}
		if connected {
			delay = c.reconnectBase // healthy connection: restart backoff
		}
		c.logger.Warn("helios: stream disconnected, will reconnect", "in", delay, "error", err)
		if err := sleepCtx(ctx, delay); err != nil {
			return err
		}
		delay *= 2
		if delay > c.reconnectMax {
			delay = c.reconnectMax
		}
	}
}

// streamOnce opens one connection and reads it until it ends. connected reports
// whether the server accepted the connection.
func (c *Client) streamOnce(ctx context.Context, handler func(FlagUpdate)) (connected bool, err error) {
	req, err := c.newRequest(ctx, http.MethodGet, "/sdk/stream", nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return false, fmt.Errorf("helios: stream request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return false, parseAPIError(resp)
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)

	var event string
	var data []string
	dispatch := func() {
		defer func() { event, data = "", nil }()
		if event != "flag_update" || len(data) == 0 {
			return
		}
		var u FlagUpdate
		if err := json.Unmarshal([]byte(strings.Join(data, "\n")), &u); err != nil {
			c.logger.Warn("helios: ignoring malformed flag_update event", "error", err)
			return
		}
		handler(u)
	}

	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case line == "":
			dispatch()
		case strings.HasPrefix(line, ":"):
			// comment / heartbeat
		case strings.HasPrefix(line, "event:"):
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	if err := scanner.Err(); err != nil {
		return true, fmt.Errorf("helios: reading stream: %w", err)
	}
	return true, errors.New("helios: stream closed by server")
}
