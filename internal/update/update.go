// Package update checks GitHub Releases for a newer godl (DESIGN.md
// section 10). It only reports; it never downloads or replaces anything.
package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// LatestURL is the GitHub API endpoint for the newest non-prerelease.
const LatestURL = "https://api.github.com/repos/zichuanxu/godl/releases/latest"

// Interval is how often the desktop app checks.
const Interval = 7 * 24 * time.Hour

// Release is a published version.
type Release struct {
	Version string `json:"version"` // "v1.2.3"
	URL     string `json:"url"`     // release notes page
}

// Latest asks url (normally LatestURL) for the newest release.
func Latest(ctx context.Context, client *http.Client, url string) (Release, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Release{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "godl-update-check")
	resp, err := client.Do(req)
	if err != nil {
		return Release{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Release{}, fmt.Errorf("release check: HTTP %s", resp.Status)
	}
	var body struct {
		TagName string `json:"tag_name"`
		HTMLURL string `json:"html_url"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(nil, resp.Body, 1<<20)).Decode(&body); err != nil {
		return Release{}, fmt.Errorf("release check: %w", err)
	}
	if _, ok := parse(body.TagName); !ok {
		return Release{}, fmt.Errorf("release check: unexpected tag %q", body.TagName)
	}
	// Only link to the project's own release pages.
	if !strings.HasPrefix(body.HTMLURL, "https://github.com/zichuanxu/godl/") {
		body.HTMLURL = "https://github.com/zichuanxu/godl/releases"
	}
	return Release{Version: body.TagName, URL: body.HTMLURL}, nil
}

// Newer reports whether candidate is a later version than current. Builds
// that are not versioned ("dev") never see updates.
func Newer(candidate, current string) bool {
	c, ok1 := parse(candidate)
	v, ok2 := parse(current)
	if !ok1 || !ok2 {
		return false
	}
	for i := range 3 {
		if c.nums[i] != v.nums[i] {
			return c.nums[i] > v.nums[i]
		}
	}
	// 1.0.0 is newer than 1.0.0-rc.1; prereleases compare as text.
	switch {
	case c.pre == v.pre:
		return false
	case c.pre == "":
		return true
	case v.pre == "":
		return false
	}
	return c.pre > v.pre
}

type version struct {
	nums [3]int
	pre  string
}

// parse reads "v1.2.3" or "1.2.3-rc.1" (build metadata ignored).
func parse(s string) (version, bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	s, _, _ = strings.Cut(s, "+")
	core, pre, _ := strings.Cut(s, "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return version{}, false
	}
	var v version
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return version{}, false
		}
		v.nums[i] = n
	}
	v.pre = pre
	return v, true
}

// State remembers the last check so the app polls at most once per Interval.
type State struct {
	CheckedAt time.Time `json:"checkedAt"`
	Latest    Release   `json:"latest"`
}

func LoadState(path string) State {
	var s State
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &s)
	}
	return s
}

func SaveState(path string, s State) error {
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Due reports whether a check is due at now.
func (s State) Due(now time.Time) bool {
	return now.Sub(s.CheckedAt) >= Interval || now.Before(s.CheckedAt)
}

// StatePath is the state file in dir.
func StatePath(dir string) string { return filepath.Join(dir, "update.json") }

var errDev = errors.New("development builds do not check for updates")

// Checker polls url at most once per Interval, remembering the last check in
// the state file at Path and in memory, so an unwritable file cannot turn
// the weekly check into a frequent one.
type Checker struct {
	Client *http.Client
	URL    string
	Path   string
	last   State
}

// Check returns the newest release and whether it is newer than current.
func (c *Checker) Check(ctx context.Context, current string, now time.Time) (Release, bool, error) {
	if _, ok := parse(current); !ok {
		return Release{}, false, errDev
	}
	s := LoadState(c.Path)
	if c.last.CheckedAt.After(s.CheckedAt) {
		s = c.last
	}
	if s.Due(now) {
		latest, err := Latest(ctx, c.Client, c.URL)
		if err != nil {
			return Release{}, false, err
		}
		s = State{CheckedAt: now, Latest: latest}
		c.last = s
		_ = SaveState(c.Path, s) // the in-memory copy still throttles
	}
	return s.Latest, Newer(s.Latest.Version, current), nil
}
