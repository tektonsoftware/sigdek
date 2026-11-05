// Package apierr turns an HTTP error response from the hosted service into a message a
// person can act on, instead of a bare status code.
package apierr

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// Error is a response with a status of 300 or more.
type Error struct {
	URL     string
	Status  int
	Message string // the server's own explanation, when it gave one
}

func (e *Error) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("%s (HTTP %d from %s)", e.Message, e.Status, e.URL)
	}
	return fmt.Sprintf("%s returned HTTP %d %s", e.URL, e.Status, http.StatusText(e.Status))
}

// Temporary reports whether retrying may help (a server error or rate limit).
func (e *Error) Temporary() bool { return e.Status >= 500 || e.Status == http.StatusTooManyRequests }

// New builds an Error from a response body like {"error": "...", "message": "..."} or
// {"error": "invalid_grant", "error_description": "..."}; any other short body is used as is.
func New(url string, status int, body []byte) *Error {
	e := &Error{URL: url, Status: status}
	var b struct {
		Error            any    `json:"error"`
		Message          string `json:"message"`
		ErrorDescription string `json:"error_description"`
	}
	if json.Unmarshal(body, &b) == nil {
		var parts []string
		switch v := b.Error.(type) {
		case string:
			parts = append(parts, v)
		case map[string]any:
			if m, ok := v["message"].(string); ok {
				parts = append(parts, m)
			}
		}
		for _, p := range []string{b.ErrorDescription, b.Message} {
			if p != "" && (len(parts) == 0 || parts[len(parts)-1] != p) {
				parts = append(parts, p)
			}
		}
		e.Message = strings.Join(parts, ": ")
		return e
	}
	if s := strings.TrimSpace(string(body)); s != "" && len(s) <= 200 && !strings.HasPrefix(s, "<") {
		e.Message = s
	}
	return e
}
