// Package quarantine marks completed downloads as coming from the internet,
// as browsers do, so opening a downloaded program still goes through
// Gatekeeper on macOS and SmartScreen on Windows.
package quarantine

import "net/url"

// origin is the source URL without its query and credentials, which may
// hold tokens.
func origin(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	u.User, u.RawQuery, u.Fragment = nil, "", ""
	return u.String()
}
