package batch

import (
	"reflect"
	"testing"
)

func TestExpand(t *testing.T) {
	cases := map[string][]string{
		"https://x/a[1-3].jpg":         {"https://x/a1.jpg", "https://x/a2.jpg", "https://x/a3.jpg"},
		"https://x/[08-10]":            {"https://x/08", "https://x/09", "https://x/10"},
		"https://x/[0-10:5]":           {"https://x/0", "https://x/5", "https://x/10"},
		"https://x/[a-c]":              {"https://x/a", "https://x/b", "https://x/c"},
		"https://x/{cd,dvd}/[1-2].iso": {"https://x/cd/1.iso", "https://x/cd/2.iso", "https://x/dvd/1.iso", "https://x/dvd/2.iso"},
		"https://x/plain":              {"https://x/plain"},
	}
	for pattern, want := range cases {
		got, err := Expand(pattern)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("Expand(%q) = %v, %v; want %v", pattern, got, err, want)
		}
	}
	for _, bad := range []string{"https://x/[1-", "https://x/[3-1]", "https://x/[a-Z]", "https://x/[1-5:0]", "https://x/[x]", "https://x/[0-99999]",
		"https://x/[9223372036854775800-9223372036854775807]", "https://x/[0-9223372036854775807:9223372036854775807]"} {
		if _, err := Expand(bad); err == nil {
			t.Errorf("Expand(%q) accepted", bad)
		}
	}
	if _, err := Expand("https://x/[1-200]/[1-200]"); err == nil {
		t.Error("40000 URLs accepted")
	}
	if !Is("https://x/[1-2]") || Is("https://x/plain") || Is("https://x/[bad") {
		t.Error("Is misclassifies")
	}
}
