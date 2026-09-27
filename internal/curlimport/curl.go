// Package curlimport turns a browser's "Copy as cURL" command into a download
// request, capturing the URL, cookies, referer, user agent, and headers
// (DESIGN.md section 6).
package curlimport

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// Request is the part of a curl command a download needs.
type Request struct {
	URL     string
	Headers map[string]string
}

// Looks reports whether text is plausibly a curl command.
func Looks(text string) bool {
	text = strings.TrimSpace(text)
	return strings.HasPrefix(text, "curl ") || strings.HasPrefix(text, "curl.exe ")
}

// Parse reads a curl command as copied from Chrome, Firefox, Safari, or Edge,
// in POSIX shell or Windows cmd quoting.
func Parse(command string) (Request, error) {
	args, err := split(command)
	if err != nil {
		return Request{}, err
	}
	if len(args) == 0 || (args[0] != "curl" && args[0] != "curl.exe") {
		return Request{}, errors.New("not a curl command")
	}
	req := Request{Headers: map[string]string{}}
	set := func(name, value string) { req.Headers[http.CanonicalHeaderKey(name)] = value }
	for i := 1; i < len(args); i++ {
		arg := args[i]
		value := func() (string, error) {
			if i+1 >= len(args) {
				return "", fmt.Errorf("%s needs a value", arg)
			}
			i++
			return args[i], nil
		}
		switch arg {
		case "-H", "--header":
			v, err := value()
			if err != nil {
				return Request{}, err
			}
			name, val, ok := strings.Cut(v, ":")
			if !ok || strings.TrimSpace(name) == "" {
				return Request{}, fmt.Errorf("invalid header %q", v)
			}
			set(strings.TrimSpace(name), strings.TrimSpace(val))
		case "-b", "--cookie":
			v, err := value()
			if err != nil {
				return Request{}, err
			}
			set("Cookie", v)
		case "-A", "--user-agent":
			v, err := value()
			if err != nil {
				return Request{}, err
			}
			set("User-Agent", v)
		case "-e", "--referer":
			v, err := value()
			if err != nil {
				return Request{}, err
			}
			set("Referer", v)
		case "-u", "--user":
			v, err := value()
			if err != nil {
				return Request{}, err
			}
			user, pass, _ := strings.Cut(v, ":")
			r := http.Request{Header: http.Header{}}
			r.SetBasicAuth(user, pass)
			set("Authorization", r.Header.Get("Authorization"))
		case "-X", "--request":
			v, err := value()
			if err != nil {
				return Request{}, err
			}
			if !strings.EqualFold(v, "GET") {
				return Request{}, fmt.Errorf("only GET requests can be downloaded, got %s", v)
			}
		case "-d", "--data", "--data-raw", "--data-binary", "--data-urlencode", "-F", "--form", "--json":
			return Request{}, errors.New("requests with a body cannot be downloaded")
		case "--url":
			v, err := value()
			if err != nil {
				return Request{}, err
			}
			req.URL = v
		case "--compressed", "-L", "--location", "-k", "--insecure", "-s", "--silent", "-i", "--include", "-g", "--globoff", "--http1.1", "--http2", "--http2-prior-knowledge", "--http3":
			// Transfer options the engine decides for itself.
		default:
			if strings.HasPrefix(arg, "-") {
				return Request{}, fmt.Errorf("unsupported curl option %s", arg)
			}
			if req.URL != "" {
				return Request{}, errors.New("more than one URL")
			}
			req.URL = arg
		}
	}
	u, err := url.Parse(req.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return Request{}, fmt.Errorf("curl command has no http or https URL")
	}
	// The engine negotiates these itself.
	for _, h := range []string{"Accept-Encoding", "Range", "If-Range", "Connection", "Host", "Content-Length"} {
		delete(req.Headers, h)
	}
	return req, nil
}

// split tokenizes a command line: POSIX single quotes, double quotes, $'...'
// strings, and backslash-newline continuations, or the cmd.exe form of
// Chrome's "Copy as cURL (cmd)".
func split(s string) ([]string, error) {
	if strings.Contains(s, "^\"") || strings.Contains(s, "^\n") {
		return splitCmd(s)
	}
	var args []string
	var cur strings.Builder
	inArg := false
	flush := func() {
		if inArg {
			args = append(args, cur.String())
			cur.Reset()
			inArg = false
		}
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\\':
			if i+1 < len(s) {
				i++
				if s[i] == '\r' && i+1 < len(s) && s[i+1] == '\n' {
					i++
				}
				if s[i] != '\n' {
					cur.WriteByte(s[i])
					inArg = true
				}
			}
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			flush()
		case c == '\'':
			end := strings.IndexByte(s[i+1:], '\'')
			if end < 0 {
				return nil, errors.New("unterminated single quote")
			}
			cur.WriteString(s[i+1 : i+1+end])
			i += end + 1
			inArg = true
		case c == '$' && i+1 < len(s) && s[i+1] == '\'':
			n, text, err := ansiC(s[i+2:])
			if err != nil {
				return nil, err
			}
			cur.WriteString(text)
			i += n + 1
			inArg = true
		case c == '"':
			inArg = true
			i++
			for ; i < len(s) && s[i] != '"'; i++ {
				if s[i] == '\\' && i+1 < len(s) && strings.IndexByte("\"\\$`\n", s[i+1]) >= 0 {
					i++
					if s[i] == '\n' {
						continue
					}
				}
				cur.WriteByte(s[i])
			}
			if i >= len(s) {
				return nil, errors.New("unterminated double quote")
			}
		default:
			cur.WriteByte(c)
			inArg = true
		}
	}
	flush()
	return args, nil
}

// splitCmd tokenizes the cmd.exe form: ^ escapes the next character, ^"
// reaches curl as a quote that groups words, \^" is a literal quote, and ^
// before a newline continues the line.
func splitCmd(s string) ([]string, error) {
	var args []string
	var cur strings.Builder
	inArg, quoted := false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '\\' && i+2 < len(s) && s[i+1] == '^' && s[i+2] == '"' {
			cur.WriteByte('"')
			i += 2
			continue
		}
		if c == '^' && i+1 < len(s) {
			i++
			c = s[i]
			switch c {
			case '\r', '\n':
				if c == '\r' && i+1 < len(s) && s[i+1] == '\n' {
					i++
				}
				continue
			case '"':
				quoted = !quoted
				inArg = true
				continue
			}
			cur.WriteByte(c)
			inArg = true
			continue
		}
		switch {
		case c == '"':
			quoted = !quoted
			inArg = true
		case !quoted && (c == ' ' || c == '\t' || c == '\n' || c == '\r'):
			if inArg {
				args = append(args, cur.String())
				cur.Reset()
				inArg = false
			}
		default:
			cur.WriteByte(c)
			inArg = true
		}
	}
	if quoted {
		return nil, errors.New("unterminated quote")
	}
	if inArg {
		args = append(args, cur.String())
	}
	return args, nil
}

// ansiC decodes the body of a $'...' string, returning the bytes consumed
// including the closing quote.
func ansiC(s string) (int, string, error) {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '\'':
			return i + 1, b.String(), nil
		case '\\':
			if i+1 >= len(s) {
				return 0, "", errors.New("unterminated $' string")
			}
			i++
			switch e := s[i]; e {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case 'r':
				b.WriteByte('\r')
			case 'x':
				var v byte
				n := 0
				for ; n < 2 && i+1 < len(s) && isHex(s[i+1]); n++ {
					i++
					v = v<<4 | hexVal(s[i])
				}
				b.WriteByte(v)
			case 'u':
				var r rune
				for n := 0; n < 4 && i+1 < len(s) && isHex(s[i+1]); n++ {
					i++
					r = r<<4 | rune(hexVal(s[i]))
				}
				b.WriteRune(r)
			default:
				b.WriteByte(e) // \\, \', \"
			}
		default:
			b.WriteByte(c)
		}
	}
	return 0, "", errors.New("unterminated $' string")
}

func isHex(c byte) bool {
	return ('0' <= c && c <= '9') || ('a' <= c && c <= 'f') || ('A' <= c && c <= 'F')
}

func hexVal(c byte) byte {
	switch {
	case c >= 'a':
		return c - 'a' + 10
	case c >= 'A':
		return c - 'A' + 10
	default:
		return c - '0'
	}
}
