package manager

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

func resolveDestination(root, rawURL, contentDisposition string) (string, error) {
	if strings.TrimSpace(root) == "" {
		return "", errors.New("download root is required")
	}
	name := filenameFromDisposition(contentDisposition)
	if name == "" {
		parsed, err := url.Parse(rawURL)
		if err != nil {
			return "", fmt.Errorf("parse download URL: %w", err)
		}
		name = filepath.Base(parsed.Path)
	}
	name = sanitizeFilename(name)
	if name == "" {
		name = "download"
	}
	category := categoryForExtension(filepath.Ext(name))
	dir := filepath.Join(root, category)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create category directory: %w", err)
	}
	candidate := filepath.Join(dir, name)
	for index := 1; ; index++ {
		if _, err := os.Lstat(candidate); errors.Is(err, os.ErrNotExist) {
			return candidate, nil
		} else if err != nil {
			return "", fmt.Errorf("check destination collision: %w", err)
		}
		extension := filepath.Ext(name)
		stem := strings.TrimSuffix(name, extension)
		candidate = filepath.Join(dir, fmt.Sprintf("%s (%d)%s", stem, index, extension))
	}
}

func filenameFromDisposition(value string) string {
	for _, part := range strings.Split(value, ";") {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(strings.ToLower(part), "filename*=") {
			value := strings.Trim(strings.TrimSpace(strings.SplitN(part, "=", 2)[1]), "\"")
			if _, decoded, ok := strings.Cut(value, "''"); ok {
				if unescaped, err := url.PathUnescape(decoded); err == nil {
					return unescaped
				}
			}
		}
	}
	for _, part := range strings.Split(value, ";") {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(strings.ToLower(part), "filename=") {
			return strings.Trim(strings.TrimSpace(strings.SplitN(part, "=", 2)[1]), "\"")
		}
	}
	return ""
}

func sanitizeFilename(name string) string {
	name = strings.TrimSpace(name)
	name = strings.ReplaceAll(name, "\\", "_")
	name = strings.ReplaceAll(name, "/", "_")
	name = strings.ReplaceAll(name, "..", "_")
	name = strings.TrimRight(name, ". ")
	if isReservedWindowsName(name) {
		name = "_" + name
	}
	for len([]byte(name)) > 240 {
		_, size := utf8.DecodeLastRuneInString(name)
		if size == 0 {
			break
		}
		name = name[:len(name)-size]
	}
	return name
}

func isReservedWindowsName(name string) bool {
	stem := strings.ToUpper(strings.TrimSuffix(name, filepath.Ext(name)))
	if stem == "CON" || stem == "PRN" || stem == "AUX" || stem == "NUL" {
		return true
	}
	if len(stem) == 4 && (strings.HasPrefix(stem, "COM") || strings.HasPrefix(stem, "LPT")) {
		return stem[3] >= '1' && stem[3] <= '9'
	}
	return false
}

func categoryForExtension(extension string) string {
	switch strings.ToLower(extension) {
	case ".mp4", ".mkv", ".webm", ".mov", ".avi":
		return "Video"
	case ".mp3", ".flac", ".wav", ".m4a", ".ogg":
		return "Music"
	case ".pdf", ".doc", ".docx", ".txt", ".md":
		return "Documents"
	case ".zip", ".tar", ".gz", ".bz2", ".7z", ".rar":
		return "Compressed"
	case ".exe", ".msi", ".dmg", ".pkg", ".deb", ".rpm":
		return "Programs"
	default:
		return "Other"
	}
}
