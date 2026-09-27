// Package batch expands batch URL patterns such as
// "https://example.com/img[001-120].jpg" into individual URLs.
//
// A pattern may hold any number of groups, expanded left to right:
//
//	[1-10]     numbers; a zero-padded start ("[01-10]") pads every number
//	[0-100:5]  numbers with a step
//	[a-z]      letters, one case
//	{a,b,c}    alternatives
package batch

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// MaxURLs caps one expansion.
const MaxURLs = 10000

// Is reports whether text contains a batch group.
func Is(text string) bool {
	parts, err := parse(text)
	return err == nil && len(parts) > 1
}

// Expand returns every URL the pattern describes, in order.
func Expand(pattern string) ([]string, error) {
	parts, err := parse(pattern)
	if err != nil {
		return nil, err
	}
	total := 1
	for _, p := range parts {
		total *= len(p)
		if total > MaxURLs {
			return nil, fmt.Errorf("pattern expands to more than %d URLs", MaxURLs)
		}
	}
	out := []string{""}
	for _, choices := range parts {
		next := make([]string, 0, len(out)*len(choices))
		for _, prefix := range out {
			for _, c := range choices {
				next = append(next, prefix+c)
			}
		}
		out = next
	}
	return out, nil
}

// parse splits the pattern into literal parts (one choice) and groups.
func parse(pattern string) ([][]string, error) {
	var parts [][]string
	for len(pattern) > 0 {
		i := strings.IndexAny(pattern, "[{")
		if i < 0 {
			parts = append(parts, []string{pattern})
			break
		}
		if i > 0 {
			parts = append(parts, []string{pattern[:i]})
		}
		closer := byte(']')
		if pattern[i] == '{' {
			closer = '}'
		}
		j := strings.IndexByte(pattern[i:], closer)
		if j < 0 {
			return nil, fmt.Errorf("unclosed %q", pattern[i])
		}
		body := pattern[i+1 : i+j]
		var choices []string
		var err error
		if closer == '}' {
			choices = strings.Split(body, ",")
		} else {
			choices, err = rangeChoices(body)
		}
		if err != nil {
			return nil, err
		}
		parts = append(parts, choices)
		pattern = pattern[i+j+1:]
	}
	return parts, nil
}

func rangeChoices(body string) ([]string, error) {
	spec, stepText, hasStep := strings.Cut(body, ":")
	from, to, ok := strings.Cut(spec, "-")
	if !ok || from == "" || to == "" {
		return nil, fmt.Errorf("invalid range [%s]; use [1-10], [01-10], [a-z], or [0-100:5]", body)
	}
	step := 1
	if hasStep {
		n, err := strconv.Atoi(stepText)
		if err != nil || n < 1 {
			return nil, fmt.Errorf("invalid step in [%s]", body)
		}
		step = n
	}
	if len(from) == 1 && len(to) == 1 && isLetter(from[0]) && isLetter(to[0]) {
		a, b := from[0], to[0]
		if (a >= 'a') != (b >= 'a') || a > b {
			return nil, fmt.Errorf("invalid letter range [%s]", body)
		}
		var out []string
		for c := int(a); c <= int(b); c += step {
			out = append(out, string(rune(c)))
		}
		return out, nil
	}
	// Nine digits keep every sum below overflow.
	if len(from) > 9 || len(to) > 9 {
		return nil, fmt.Errorf("numbers in [%s] are too long", body)
	}
	a, errA := strconv.Atoi(from)
	b, errB := strconv.Atoi(to)
	if errA != nil || errB != nil || a < 0 || b < a {
		return nil, fmt.Errorf("invalid number range [%s]", body)
	}
	count := (b-a)/step + 1
	if count > MaxURLs {
		return nil, errors.New("range is too large")
	}
	width := 0
	if len(from) > 1 && from[0] == '0' {
		width = len(from)
	}
	out := make([]string, 0, count)
	for k := range count {
		out = append(out, fmt.Sprintf("%0*d", width, a+k*step))
	}
	return out, nil
}

func isLetter(c byte) bool { return ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z') }
