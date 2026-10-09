package helios

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
)

// Evaluation.Reason values. Helios may add more, so treat unknown reasons as
// informational.
const (
	// ReasonFlagNotFound is returned for an unknown or deleted flag. Value is
	// nil in that case.
	ReasonFlagNotFound = "FLAG_NOT_FOUND"

	// ReasonFlagDisabled is returned when the flag is switched off in this
	// environment.
	ReasonFlagDisabled = "FLAG_DISABLED"

	// ReasonTypeMismatch is returned when the flag's value does not fit its
	// declared type.
	ReasonTypeMismatch = "TYPE_MISMATCH"
)

// Subject is the user, device or account a flag is evaluated for. Key is what
// Helios hashes to put the subject in a rollout bucket, so it must be stable
// (a user ID, not a session ID) for the subject to see a consistent variation.
type Subject struct {
	// Key uniquely identifies the subject. Required.
	Key string `json:"subjectKey"`

	// Attributes are optional values used by targeting rules, for example
	// {"country": "IN", "plan": "pro"}.
	Attributes map[string]any `json:"attributes,omitempty"`
}

// Evaluation is the result of evaluating one flag for one subject.
type Evaluation struct {
	// Value is the flag value Helios chose: a bool, string, number, or
	// decoded JSON. It is nil when the flag was not found.
	Value any `json:"value"`

	// FlagKey is the flag this result is for.
	FlagKey string `json:"flagKey"`

	// VariationID identifies the chosen variation. It is empty when no
	// variation applies.
	VariationID string `json:"variationId"`

	// Reason says why this value was chosen, for example ReasonFlagNotFound.
	Reason string `json:"reason"`

	// ExperimentID is set when the evaluation is part of an experiment, and
	// empty otherwise.
	ExperimentID string `json:"experimentId"`
}

// evaluateRequest and evaluateResponse mirror POST /evaluate in Helios'
// api/openapi.yaml. The API accepts up to 100 flag keys per call; the SDK
// sends one.
type evaluateRequest struct {
	Context  Subject  `json:"context"`
	FlagKeys []string `json:"flagKeys"`
}

type evaluateResponse struct {
	Evaluations []Evaluation `json:"evaluations"`
}

// Evaluate asks Helios for the value of flagKey for subject.
//
// It retries network errors, HTTP 429 and HTTP 5xx (see WithMaxRetries) and
// returns an error if all attempts fail. If you would rather never handle an
// error, use BoolVariation, StringVariation, IntVariation or Float64Variation,
// which return a default instead.
func (c *Client) Evaluate(ctx context.Context, flagKey string, subject Subject) (*Evaluation, error) {
	if flagKey == "" {
		return nil, ErrMissingFlagKey
	}
	if subject.Key == "" {
		return nil, ErrMissingSubjectKey
	}

	payload, err := json.Marshal(evaluateRequest{Context: subject, FlagKeys: []string{flagKey}})
	if err != nil {
		return nil, fmt.Errorf("helios: encoding request: %w", err)
	}

	var lastErr error
	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		if attempt > 0 {
			// Exponential backoff: 100ms, 200ms, 400ms, ...
			if err := sleepCtx(ctx, c.retryBase<<(attempt-1)); err != nil {
				return nil, err
			}
		}
		ev, retryable, err := c.evaluateOnce(ctx, flagKey, payload)
		if err == nil {
			return ev, nil
		}
		lastErr = err
		if !retryable || ctx.Err() != nil {
			break
		}
		c.logger.Debug("helios: evaluate attempt failed", "attempt", attempt+1, "error", err)
	}
	return nil, lastErr
}

// evaluateOnce performs a single attempt. The bool reports whether the failure
// is worth retrying.
func (c *Client) evaluateOnce(ctx context.Context, flagKey string, payload []byte) (*Evaluation, bool, error) {
	if c.requestTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.requestTimeout)
		defer cancel()
	}

	req, err := c.newRequest(ctx, http.MethodPost, "/evaluate", bytes.NewReader(payload))
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, true, fmt.Errorf("helios: evaluate request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		apiErr := parseAPIError(resp)
		retryable := resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500
		return nil, retryable, apiErr
	}

	var out evaluateResponse
	body := io.LimitReader(resp.Body, maxResponseBytes)
	if err := json.NewDecoder(body).Decode(&out); err != nil {
		return nil, false, fmt.Errorf("helios: decoding response: %w", err)
	}
	for i := range out.Evaluations {
		if out.Evaluations[i].FlagKey == flagKey {
			return &out.Evaluations[i], false, nil
		}
	}
	return nil, false, fmt.Errorf("helios: response has no evaluation for flag %q", flagKey)
}

// BoolVariation returns the boolean value of flagKey for subject, or def if
// Helios is unreachable, the flag does not exist, or its value is not a bool.
func (c *Client) BoolVariation(ctx context.Context, flagKey string, subject Subject, def bool) bool {
	v, ok := c.value(ctx, flagKey, subject)
	if !ok {
		return def
	}
	if b, ok := v.(bool); ok {
		return b
	}
	return def
}

// StringVariation returns the string value of flagKey for subject, or def if
// Helios is unreachable, the flag does not exist, or its value is not a string.
func (c *Client) StringVariation(ctx context.Context, flagKey string, subject Subject, def string) string {
	v, ok := c.value(ctx, flagKey, subject)
	if !ok {
		return def
	}
	if s, ok := v.(string); ok {
		return s
	}
	return def
}

// Float64Variation returns the numeric value of flagKey for subject, or def if
// Helios is unreachable, the flag does not exist, or its value is not a number.
func (c *Client) Float64Variation(ctx context.Context, flagKey string, subject Subject, def float64) float64 {
	v, ok := c.value(ctx, flagKey, subject)
	if !ok {
		return def
	}
	if f, ok := v.(float64); ok {
		return f
	}
	return def
}

// IntVariation returns the integer value of flagKey for subject, or def if
// Helios is unreachable, the flag does not exist, or its value is not a whole
// number (for example 2.5 returns def).
func (c *Client) IntVariation(ctx context.Context, flagKey string, subject Subject, def int) int {
	v, ok := c.value(ctx, flagKey, subject)
	if !ok {
		return def
	}
	f, ok := v.(float64)
	if !ok || f != math.Trunc(f) || math.Abs(f) > 1<<53 {
		return def
	}
	return int(f)
}

// value evaluates a flag and reports whether a usable value came back.
func (c *Client) value(ctx context.Context, flagKey string, subject Subject) (any, bool) {
	ev, err := c.Evaluate(ctx, flagKey, subject)
	if err != nil {
		if errors.Is(err, ErrUnauthorized) {
			c.logger.Error("helios: SDK key rejected, serving default values", "error", err)
		} else {
			c.logger.Warn("helios: evaluation failed, serving default value", "flag", flagKey, "error", err)
		}
		return nil, false
	}
	if ev.Reason == ReasonFlagNotFound || ev.Value == nil {
		return nil, false
	}
	return ev.Value, true
}
