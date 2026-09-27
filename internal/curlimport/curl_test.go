package curlimport

import "testing"

func TestParseBrowserCommands(t *testing.T) {
	cases := map[string]string{
		"chrome bash": `curl 'https://cdn.example.com/file.zip?sig=a%20b' \
  -H 'accept: */*' \
  -H 'accept-language: en-US,en;q=0.9' \
  -b 'session=abc; theme=dark' \
  -H 'referer: https://example.com/page' \
  -H 'user-agent: Mozilla/5.0 (Macintosh)' \
  --compressed`,
		"firefox": `curl 'https://cdn.example.com/file.zip?sig=a%20b' -H 'User-Agent: Mozilla/5.0 (Macintosh)' -H 'Accept: */*' -H 'Accept-Language: en-US,en;q=0.9' -H 'Accept-Encoding: gzip, deflate, br' -H 'Referer: https://example.com/page' -H 'Connection: keep-alive' -H 'Cookie: session=abc; theme=dark'`,
		"chrome cmd": "curl ^\"https://cdn.example.com/file.zip?sig=a%20b^\" ^\r\n" +
			"  -H ^\"accept: */*^\" ^\r\n" +
			"  -H ^\"accept-language: en-US,en;q=0.9^\" ^\r\n" +
			"  -b ^\"session=abc; theme=dark^\" ^\r\n" +
			"  -H ^\"referer: https://example.com/page^\" ^\r\n" +
			"  -H ^\"user-agent: Mozilla/5.0 (Macintosh)^\"",
		"ansi-c": `curl $'https://cdn.example.com/file.zip?sig=a%20b' -H $'referer: https://example.com/page' -H $'cookie: session=abc; theme=dark' -A $'Mozilla/5.0 \x28Macintosh\x29'`,
	}
	for name, command := range cases {
		req, err := Parse(command)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if req.URL != "https://cdn.example.com/file.zip?sig=a%20b" {
			t.Errorf("%s: URL = %q", name, req.URL)
		}
		want := map[string]string{
			"Cookie":     "session=abc; theme=dark",
			"Referer":    "https://example.com/page",
			"User-Agent": "Mozilla/5.0 (Macintosh)",
		}
		for k, v := range want {
			if req.Headers[k] != v {
				t.Errorf("%s: %s = %q, want %q", name, k, req.Headers[k], v)
			}
		}
		for _, dropped := range []string{"Accept-Encoding", "Connection"} {
			if _, ok := req.Headers[dropped]; ok {
				t.Errorf("%s: %s kept", name, dropped)
			}
		}
	}
}

func TestParseRejects(t *testing.T) {
	for _, command := range []string{
		`wget https://example.com/a`,
		`curl 'https://example.com/a' --data-raw 'x=1'`,
		`curl -X POST https://example.com/a`,
		`curl ftp://example.com/a`,
		`curl 'https://example.com/a`,
		`curl https://example.com/a --upload-file x`,
	} {
		if _, err := Parse(command); err == nil {
			t.Errorf("accepted %q", command)
		}
	}
	if req, err := Parse(`curl -u me:pw https://example.com/a`); err != nil || req.Headers["Authorization"] != "Basic bWU6cHc=" {
		t.Fatalf("basic auth: %+v %v", req, err)
	}
	if !Looks("  curl 'x'") || Looks("https://example.com") {
		t.Fatal("Looks misclassifies")
	}
}

func TestParseCmdEscapes(t *testing.T) {
	req, err := Parse(`curl ^"https://example.com/a?x=1^&y=2^%^" -H ^"x-json: ^{\^"a\^":1^}^"`)
	if err != nil {
		t.Fatal(err)
	}
	if req.URL != "https://example.com/a?x=1&y=2%" || req.Headers["X-Json"] != `{"a":1}` {
		t.Fatalf("got %+v", req)
	}
}
