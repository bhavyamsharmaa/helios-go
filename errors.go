package helios

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Sentinel errors. Compare with errors.Is.
var (
	// ErrMissingSDKKey is returned by New when the SDK key is empty.
	ErrMissingSDKKey = errors.New("helios: SDK key is required")

	// ErrMissingBaseURL is returned by New when no base URL was given via
	// WithBaseURL or the HELIOS_BASE_URL environment variable.
	ErrMissingBaseURL = errors.New("helios: base URL is required (use WithBaseURL or set HELIOS_BASE_URL)")

	// ErrInvalidBaseURL is returned by New when the base URL is not an
	// absolute http(s) URL.
	ErrInvalidBaseURL = errors.New("helios: invalid base URL")

	// ErrMissingFlagKey is returned when a flag key is empty.
	ErrMissingFlagKey = errors.New("helios: flag key is required")

	// ErrMissingSubjectKey is returned when Subject.Key is empty.
	ErrMissingSubjectKey = errors.New("helios: subject key is required")

	// ErrUnauthorized matches any *APIError with HTTP status 401 or 403,
	// which means the SDK key is wrong, revoked, or lacks access.
	ErrUnauthorized = errors.New("helios: invalid or unauthorized SDK key")
)

// APIError is returned when the Helios API answers with a non-2xx status.
type APIError struct {
	StatusCode int
	Message    string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("helios: API returned status %d: %s", e.StatusCode, e.Message)
}

// Is lets errors.Is(err, ErrUnauthorized) match 401 and 403 responses.
func (e *APIError) Is(target error) bool {
	return target == ErrUnauthorized &&
		(e.StatusCode == http.StatusUnauthorized || e.StatusCode == http.StatusForbidden)
}

// parseAPIError builds an APIError from a non-2xx response. It understands
// Helios' {"code", "message"} bodies (and a bare {"error": "..."}) and falls back to the raw
// text or the HTTP status text.
func parseAPIError(resp *http.Response) *APIError {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	msg := strings.TrimSpace(string(raw))

	var body struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	if json.Unmarshal(raw, &body) == nil {
		switch {
		case body.Error != "":
			msg = body.Error
		case body.Message != "":
			msg = body.Message
		}
	}
	if msg == "" {
		msg = http.StatusText(resp.StatusCode)
	}
	return &APIError{StatusCode: resp.StatusCode, Message: msg}
}
