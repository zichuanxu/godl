package manager

import (
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// maxNameBytes stays under the 255-byte name limit of common file systems
// with room for " (NNN)" and the ".part.meta" suffix.
const maxNameBytes = 200

// fileName resolves a download's file name (DESIGN.md 4.4): the server's
// Content-Disposition name, then the last URL path segment, then the host.
func fileName(disposition, rawURL string) string {
	if name := sanitizeFilename(disposition); name != "" {
		return name
	}
	if u, err := url.Parse(rawURL); err == nil {
		if base := path.Base(u.Path); base != "/" && base != "." {
			if name := sanitizeFilename(base); name != "" {
				return name
			}
		}
		if name := sanitizeFilename(u.Hostname()); name != "" {
			return name
		}
	}
	return "download"
}

// sanitizeFilename turns attacker-controlled input into one safe path
// element, or "" when nothing usable remains.
func sanitizeFilename(name string) string {
	name = norm.NFC.String(name)
	name = strings.Map(func(r rune) rune {
		switch {
		// Cf covers bidi overrides that make "x\u202Efdp.exe" display as a PDF.
		case r == '/' || r == '\\' || strings.ContainsRune(`<>:"|?*`, r) || unicode.IsControl(r) || unicode.Is(unicode.Cf, r):
			return '_'
		case r == utf8.RuneError:
			return -1
		}
		return r
	}, name)
	name = strings.TrimSpace(name)
	name = strings.TrimLeft(name, ".") // no hidden files, no "..", no "."
	name = strings.TrimRight(name, ". ")
	if name == "" {
		return ""
	}
	stem, _, _ := strings.Cut(name, ".")
	if reservedWindowsName(strings.TrimSpace(stem)) {
		name = "_" + name
	}
	if len(name) > maxNameBytes {
		ext := filepath.Ext(name)
		if len(ext) > 16 {
			ext = ""
		}
		stem := strings.TrimSuffix(name, ext)
		stem = stem[:maxNameBytes-len(ext)]
		for !utf8.ValidString(stem) { // never split a multi-byte rune
			stem = stem[:len(stem)-1]
		}
		name = strings.TrimRight(stem, ". ") + ext
	}
	return name
}

// reservedWindowsName reports device names Windows reserves with or without
// an extension.
func reservedWindowsName(stem string) bool {
	switch s := strings.ToUpper(stem); s {
	case "CON", "PRN", "AUX", "NUL", "CONIN$", "CONOUT$":
		return true
	default:
		prefix, digit := s[:min(3, len(s))], s[min(3, len(s)):]
		return (prefix == "COM" || prefix == "LPT") && utf8.RuneCountInString(digit) == 1 && strings.ContainsAny(digit, "0123456789¹²³")
	}
}

var categories = map[string][]string{
	"Video":      {".mp4", ".mkv", ".webm", ".mov", ".avi", ".wmv", ".flv", ".m4v", ".ts", ".mpg", ".mpeg"},
	"Music":      {".mp3", ".flac", ".wav", ".m4a", ".aac", ".ogg", ".opus", ".wma"},
	"Documents":  {".pdf", ".doc", ".docx", ".xls", ".xlsx", ".ppt", ".pptx", ".txt", ".md", ".epub", ".odt", ".rtf", ".csv"},
	"Compressed": {".zip", ".tar", ".gz", ".tgz", ".bz2", ".xz", ".zst", ".7z", ".rar", ".iso"},
	"Programs":   {".exe", ".msi", ".dmg", ".pkg", ".deb", ".rpm", ".appimage", ".apk"},
}

// category routes a file name to a folder under the download root; other
// files go to the root itself.
func category(name string) string {
	ext := strings.ToLower(filepath.Ext(name))
	for folder, exts := range categories {
		for _, e := range exts {
			if e == ext {
				return folder
			}
		}
	}
	return ""
}

// helperSuffixes name the files the engine keeps beside a destination.
var helperSuffixes = []string{".part", ".part.meta", ".lock"}

func helperPaths(dest string) []string {
	paths := make([]string, len(helperSuffixes))
	for i, suffix := range helperSuffixes {
		paths[i] = dest + suffix
	}
	return paths
}

// clashes reports whether two destinations would share a file: the same
// path, or one's helper file being the other.
func clashes(a, b string) bool {
	if samePath(a, b) {
		return true
	}
	for _, suffix := range helperSuffixes {
		if samePath(a+suffix, b) || samePath(a, b+suffix) {
			return true
		}
	}
	return false
}

// freeName returns dir/name, or "name (N).ext" when that path is taken by a
// file, partial state, or another queued download. The caller holds the
// manager lock, so two adds cannot pick the same name; the engine's
// link-based commit (DESIGN.md 3.5) keeps another process from being
// overwritten.
func freeName(dir, name string, taken func(string) bool) (string, error) {
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for i := 0; i < 10000; i++ {
		candidate := name
		if i > 0 {
			candidate = fmt.Sprintf("%s (%d)%s", stem, i, ext)
		}
		dest := filepath.Join(dir, candidate)
		if taken(dest) {
			continue
		}
		free := true
		for _, p := range append([]string{dest}, helperPaths(dest)...) {
			if _, err := os.Lstat(p); err == nil {
				free = false
			} else if !errors.Is(err, fs.ErrNotExist) {
				return "", fmt.Errorf("check destination: %w", err)
			}
		}
		if free {
			return dest, nil
		}
	}
	return "", fmt.Errorf("no free file name for %s in %s", name, dir)
}
