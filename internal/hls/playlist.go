package hls

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net/url"
	"reflect"
	"strconv"
	"strings"
)

// item is one resource written to the output, in order: a media segment or an
// EXT-X-MAP init section. The item list is JSON-encoded to fingerprint a
// playlist for resume.
type item struct {
	URI    string `json:"uri"`
	Off    int64  `json:"off,omitempty"`
	Len    int64  `json:"len"`           // -1: the whole resource
	KeyURI string `json:"key,omitempty"` // AES-128 key URI; empty when clear
	IV     []byte `json:"iv,omitempty"`
}

type keyTag struct {
	uri string
	iv  []byte // nil: derive from the media sequence number
}

// parsePlaylist parses a playlist fetched from base. A master playlist yields
// the URL of its best variant; a media playlist yields its items.
//
// ponytail: alternate renditions (EXT-X-MEDIA audio/subtitles) are ignored;
// only the variant's own stream is downloaded.
func parsePlaylist(body []byte, base *url.URL) (variant string, items []item, err error) {
	var (
		lineNo           int
		bestBW, bestArea int64 = -1, -1
		streamInf        map[string]string
		mediaSeq, segN   int64
		sawInf, endList  bool
		br               *[2]int64 // pending EXT-X-BYTERANGE {length, offset or -1}
		prev             item      // previous media segment
		key              *keyTag
		curMap, lastMap  *item
		errorf           = func(format string, a ...any) error {
			return fmt.Errorf("playlist line %d: "+format, append([]any{lineNo}, a...)...)
		}
	)
	sc := bufio.NewScanner(bytes.NewReader(body))
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if lineNo == 1 {
			if strings.TrimPrefix(line, "\xef\xbb\xbf") != "#EXTM3U" {
				return "", nil, fmt.Errorf("not an HLS playlist: missing #EXTM3U")
			}
			continue
		}
		tag, value, _ := strings.Cut(line, ":")
		switch {
		case line == "":
		case tag == "#EXT-X-STREAM-INF":
			streamInf = parseAttrs(value)
		case tag == "#EXTINF":
			dur, _, _ := strings.Cut(value, ",")
			if d, err := strconv.ParseFloat(strings.TrimSpace(dur), 64); err != nil || d < 0 {
				return "", nil, errorf("malformed EXTINF %q", value)
			}
			sawInf = true
		case tag == "#EXT-X-MEDIA-SEQUENCE":
			if mediaSeq, err = strconv.ParseInt(value, 10, 64); err != nil || mediaSeq < 0 {
				return "", nil, errorf("malformed EXT-X-MEDIA-SEQUENCE %q", value)
			}
		case tag == "#EXT-X-BYTERANGE":
			n, o, ok := parseByteRange(value)
			if !ok {
				return "", nil, errorf("malformed EXT-X-BYTERANGE %q", value)
			}
			br = &[2]int64{n, o}
		case tag == "#EXT-X-KEY":
			if key, err = parseKey(parseAttrs(value), base); err != nil {
				return "", nil, errorf("%w", err)
			}
		case tag == "#EXT-X-MAP":
			m, err := parseMap(parseAttrs(value), base, key)
			if err != nil {
				return "", nil, errorf("%w", err)
			}
			if curMap == nil || !reflect.DeepEqual(m, *curMap) {
				curMap = &m
			}
		case line == "#EXT-X-ENDLIST":
			endList = true
		case strings.HasPrefix(line, "#"):
			// Comments and unknown tags are ignored (RFC 8216 section 4.1).
		case streamInf != nil:
			u, err := base.Parse(line)
			if err != nil {
				return "", nil, errorf("bad variant URI %q: %v", line, err)
			}
			bw, _ := strconv.ParseInt(streamInf["BANDWIDTH"], 10, 64)
			area := resolutionArea(streamInf["RESOLUTION"])
			if bw > bestBW || bw == bestBW && area > bestArea {
				variant, bestBW, bestArea = u.String(), bw, area
			}
			streamInf = nil
		default:
			if !sawInf {
				return "", nil, errorf("segment %q has no EXTINF", line)
			}
			u, err := base.Parse(line)
			if err != nil {
				return "", nil, errorf("bad segment URI %q: %v", line, err)
			}
			it := item{URI: u.String(), Len: -1}
			if br != nil {
				it.Len, it.Off = br[0], br[1]
				if it.Off < 0 {
					if prev.URI != it.URI || prev.Len < 0 {
						return "", nil, errorf("EXT-X-BYTERANGE without offset must follow a sub-range of the same resource")
					}
					it.Off = prev.Off + prev.Len
				}
			}
			if key != nil {
				it.KeyURI, it.IV = key.uri, key.iv
				if it.IV == nil {
					it.IV = seqIV(mediaSeq + segN)
				}
			}
			if curMap != nil && curMap != lastMap {
				items = append(items, *curMap)
				lastMap = curMap
			}
			items = append(items, it)
			prev, segN, sawInf, br = it, segN+1, false, nil
		}
	}
	if err := sc.Err(); err != nil {
		return "", nil, fmt.Errorf("read playlist: %w", err)
	}
	if variant != "" {
		return variant, nil, nil
	}
	if !endList {
		return "", nil, ErrLive
	}
	if segN == 0 {
		return "", nil, fmt.Errorf("playlist has no segments")
	}
	return "", items, nil
}

func parseKey(attrs map[string]string, base *url.URL) (*keyTag, error) {
	switch method := attrs["METHOD"]; method {
	case "NONE":
		return nil, nil
	case "AES-128":
	default:
		return nil, fmt.Errorf("%w: EXT-X-KEY METHOD=%s", ErrUnsupported, method)
	}
	if f := attrs["KEYFORMAT"]; f != "" && f != "identity" {
		return nil, fmt.Errorf("%w: EXT-X-KEY KEYFORMAT=%q", ErrUnsupported, f)
	}
	if attrs["URI"] == "" {
		return nil, fmt.Errorf("EXT-X-KEY has no URI")
	}
	u, err := base.Parse(attrs["URI"])
	if err != nil {
		return nil, fmt.Errorf("bad key URI: %v", err)
	}
	k := &keyTag{uri: u.String()}
	if s, ok := attrs["IV"]; ok {
		if k.iv, err = parseIV(s); err != nil {
			return nil, err
		}
	}
	return k, nil
}

func parseMap(attrs map[string]string, base *url.URL, key *keyTag) (item, error) {
	if attrs["URI"] == "" {
		return item{}, fmt.Errorf("EXT-X-MAP has no URI")
	}
	u, err := base.Parse(attrs["URI"])
	if err != nil {
		return item{}, fmt.Errorf("bad EXT-X-MAP URI: %v", err)
	}
	m := item{URI: u.String(), Len: -1}
	if s, ok := attrs["BYTERANGE"]; ok {
		n, o, ok := parseByteRange(s)
		if !ok {
			return item{}, fmt.Errorf("malformed EXT-X-MAP BYTERANGE %q", s)
		}
		m.Len, m.Off = n, max(o, 0)
	}
	if key != nil {
		if key.iv == nil { // RFC 8216 section 4.3.2.5
			return item{}, fmt.Errorf("%w: encrypted EXT-X-MAP without an IV", ErrUnsupported)
		}
		m.KeyURI, m.IV = key.uri, key.iv
	}
	return m, nil
}

// parseAttrs parses an attribute list: KEY=value,KEY="quoted, value".
func parseAttrs(s string) map[string]string {
	attrs := map[string]string{}
	for s != "" {
		k, rest, ok := strings.Cut(s, "=")
		if !ok {
			break
		}
		var v string
		if strings.HasPrefix(rest, `"`) {
			if end := strings.IndexByte(rest[1:], '"'); end >= 0 {
				v, rest = rest[1:1+end], rest[2+end:]
			} else {
				v, rest = rest[1:], ""
			}
		} else {
			v, rest, _ = strings.Cut(rest, ",")
		}
		attrs[strings.TrimSpace(k)] = strings.TrimSpace(v)
		s = strings.TrimPrefix(strings.TrimSpace(rest), ",")
	}
	return attrs
}

// parseByteRange parses "n[@o]"; o is -1 when absent.
func parseByteRange(s string) (n, o int64, ok bool) {
	ns, offS, hasOff := strings.Cut(strings.TrimSpace(s), "@")
	n, err := strconv.ParseInt(ns, 10, 64)
	if err != nil || n <= 0 {
		return 0, 0, false
	}
	if !hasOff {
		return n, -1, true
	}
	o, err = strconv.ParseInt(offS, 10, 64)
	return n, o, err == nil && o >= 0
}

func parseIV(s string) ([]byte, error) {
	h := strings.TrimPrefix(strings.TrimPrefix(s, "0x"), "0X")
	if len(h) == 0 || len(h) > 32 {
		return nil, fmt.Errorf("malformed IV %q", s)
	}
	iv, err := hex.DecodeString(strings.Repeat("0", 32-len(h)) + h)
	if err != nil {
		return nil, fmt.Errorf("malformed IV %q", s)
	}
	return iv, nil
}

// seqIV is the default IV: the media sequence number as a 128-bit big-endian
// integer (RFC 8216 section 5.2).
func seqIV(seq int64) []byte {
	iv := make([]byte, 16)
	binary.BigEndian.PutUint64(iv[8:], uint64(seq))
	return iv
}

func resolutionArea(s string) int64 {
	w, h, _ := strings.Cut(s, "x")
	wi, err1 := strconv.ParseInt(w, 10, 64)
	hi, err2 := strconv.ParseInt(h, 10, 64)
	if err1 != nil || err2 != nil {
		return 0
	}
	return wi * hi
}
