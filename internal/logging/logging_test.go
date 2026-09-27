package logging

import (
	"strings"
	"testing"
)

func TestRedactURLScrubsCredentials(t *testing.T) {
	raw := "https://user:pass@bucket.s3.amazonaws.com/object.bin?X-Amz-Signature=abc123&X-Amz-Credential=AKIA&token=t0k&sig=s1g&part=7#frag"
	got := RedactURL(raw)
	for _, secret := range []string{"pass", "abc123", "AKIA", "t0k", "s1g", "frag"} {
		if strings.Contains(got, secret) {
			t.Fatalf("RedactURL leaked %q: %s", secret, got)
		}
	}
	if !strings.Contains(got, "part=7") || !strings.Contains(got, "bucket.s3.amazonaws.com/object.bin") {
		t.Fatalf("RedactURL removed non-secret parts: %s", got)
	}
	if got := RedactURL("://bad"); got != "[unparseable URL]" {
		t.Fatalf("RedactURL(bad) = %q", got)
	}
}
