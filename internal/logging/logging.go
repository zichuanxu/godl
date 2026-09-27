// Package logging builds the structured service logger and scrubs secrets
// from values that are safe to log only in redacted form.
package logging

import (
	"io"
	"log/slog"
	"net/url"
	"strings"
)

// New returns a JSON logger. Callers must never pass request headers, cookies,
// or raw URLs as attributes; use RedactURL for URLs.
func New(w io.Writer, level slog.Level) *slog.Logger {
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level}))
}

// sensitiveFragments marks query parameters whose values are credentials, such
// as X-Amz-Signature, X-Goog-Credential, token, sig, or api_key.
var sensitiveFragments = []string{"sig", "token", "key", "secret", "password", "credential", "auth", "session"}

const redacted = "REDACTED"

// RedactURL removes user info and the values of credential-like query
// parameters so signed URLs can be logged and pasted into public issues.
func RedactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "[unparseable URL]"
	}
	if u.User != nil {
		u.User = url.User(redacted)
	}
	query := u.Query()
	for name, values := range query {
		lower := strings.ToLower(name)
		for _, fragment := range sensitiveFragments {
			if strings.Contains(lower, fragment) {
				for i := range values {
					values[i] = redacted
				}
				break
			}
		}
	}
	u.RawQuery = query.Encode()
	u.Fragment = ""
	return u.String()
}
