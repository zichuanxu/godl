package engine

import (
	"strconv"
	"strings"
)

func strongETag(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || strings.HasPrefix(value, "W/") || len(value) < 2 || value[0] != '"' || value[len(value)-1] != '"' {
		return ""
	}
	return value
}

func parseContentRange(value string) (start, end, total int64, ok bool) {
	fields := strings.Fields(strings.TrimSpace(value))
	if len(fields) != 2 || !strings.EqualFold(fields[0], "bytes") {
		return 0, 0, 0, false
	}
	parts := strings.SplitN(fields[1], "/", 2)
	if len(parts) != 2 || parts[0] == "*" || parts[1] == "*" {
		return 0, 0, 0, false
	}
	span := strings.SplitN(parts[0], "-", 2)
	if len(span) != 2 {
		return 0, 0, 0, false
	}
	start, errStart := strconv.ParseInt(span[0], 10, 64)
	end, errEnd := strconv.ParseInt(span[1], 10, 64)
	total, errTotal := strconv.ParseInt(parts[1], 10, 64)
	if errStart != nil || errEnd != nil || errTotal != nil || start < 0 || end < start || total <= end {
		return 0, 0, 0, false
	}
	return start, end, total, true
}

// parseUnsatisfiedContentRange reads the total from "bytes */total".
func parseUnsatisfiedContentRange(value string) (int64, bool) {
	fields := strings.Fields(strings.TrimSpace(value))
	if len(fields) != 2 || !strings.EqualFold(fields[0], "bytes") {
		return 0, false
	}
	total, ok := strings.CutPrefix(fields[1], "*/")
	if !ok {
		return 0, false
	}
	n, err := strconv.ParseInt(total, 10, 64)
	return n, err == nil && n >= 0
}
