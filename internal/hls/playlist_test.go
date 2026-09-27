package hls

import (
	"errors"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

func mustURL(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestParseMaster(t *testing.T) {
	body := `#EXTM3U
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="a",URI="audio.m3u8"
#EXT-X-STREAM-INF:BANDWIDTH=800000,RESOLUTION=640x360,CODECS="avc1.4d401e,mp4a.40.2"
low/index.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=2000000,RESOLUTION=1280x720
mid/index.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=2000000,RESOLUTION=1920x1080
../hi/index.m3u8
#EXT-X-I-FRAME-STREAM-INF:BANDWIDTH=9000000,URI="iframe.m3u8"
`
	variant, items, err := parsePlaylist([]byte(body), mustURL(t, "https://cdn.example/v/master.m3u8"))
	if err != nil || items != nil {
		t.Fatalf("got %v, %v", items, err)
	}
	if want := "https://cdn.example/hi/index.m3u8"; variant != want {
		t.Fatalf("variant = %q, want %q", variant, want)
	}
}

func TestParseMedia(t *testing.T) {
	base := mustURL(t, "https://cdn.example/a/b/media.m3u8")
	iv := make([]byte, 16)
	iv[15] = 0xab
	tests := []struct {
		name string
		body string
		want []item
	}{
		{
			name: "clear with relative URIs",
			body: "#EXTM3U\n#EXT-X-TARGETDURATION:4\n#EXT-X-UNKNOWN-TAG:1\n#EXTINF:4.0,\nseg0.ts\n#EXTINF:4,title\n../c/seg1.ts\n#EXTINF:3.5,\nhttps://other.example/seg2.ts?x=1\n#EXT-X-ENDLIST\n",
			want: []item{
				{URI: "https://cdn.example/a/b/seg0.ts", Len: -1},
				{URI: "https://cdn.example/a/c/seg1.ts", Len: -1},
				{URI: "https://other.example/seg2.ts?x=1", Len: -1},
			},
		},
		{
			name: "keys with explicit IV, sequence IV and NONE",
			body: `#EXTM3U
#EXT-X-MEDIA-SEQUENCE:7
#EXT-X-KEY:METHOD=AES-128,URI="k1.key",IV=0xab
#EXTINF:4,
s7.ts
#EXT-X-KEY:METHOD=AES-128,URI="/keys/k2.key",KEYFORMAT="identity"
#EXTINF:4,
s8.ts
#EXT-X-KEY:METHOD=NONE
#EXTINF:4,
s9.ts
#EXT-X-ENDLIST`,
			want: []item{
				{URI: "https://cdn.example/a/b/s7.ts", Len: -1, KeyURI: "https://cdn.example/a/b/k1.key", IV: iv},
				{URI: "https://cdn.example/a/b/s8.ts", Len: -1, KeyURI: "https://cdn.example/keys/k2.key", IV: seqIV(8)},
				{URI: "https://cdn.example/a/b/s9.ts", Len: -1},
			},
		},
		{
			name: "map and byteranges with implicit offsets",
			body: `#EXTM3U
#EXT-X-MAP:URI="main.mp4",BYTERANGE="100@0"
#EXTINF:4,
#EXT-X-BYTERANGE:500@100
main.mp4
#EXTINF:4,
#EXT-X-BYTERANGE:300
main.mp4
#EXT-X-MAP:URI="main.mp4",BYTERANGE="100@0"
#EXTINF:4,
#EXT-X-BYTERANGE:200
main.mp4
#EXT-X-MAP:URI="init2.mp4"
#EXTINF:4,
other.m4s
#EXT-X-ENDLIST
`,
			want: []item{
				{URI: "https://cdn.example/a/b/main.mp4", Len: 100},
				{URI: "https://cdn.example/a/b/main.mp4", Off: 100, Len: 500},
				{URI: "https://cdn.example/a/b/main.mp4", Off: 600, Len: 300},
				{URI: "https://cdn.example/a/b/main.mp4", Off: 900, Len: 200},
				{URI: "https://cdn.example/a/b/init2.mp4", Len: -1},
				{URI: "https://cdn.example/a/b/other.m4s", Len: -1},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			variant, items, err := parsePlaylist([]byte(tt.body), base)
			if err != nil || variant != "" {
				t.Fatalf("variant %q, err %v", variant, err)
			}
			if !reflect.DeepEqual(items, tt.want) {
				t.Fatalf("items:\n got %+v\nwant %+v", items, tt.want)
			}
		})
	}
}

func TestParseErrors(t *testing.T) {
	tests := []struct {
		name, body string
		is         error
		contains   string
	}{
		{"live", "#EXTM3U\n#EXTINF:4,\ns.ts\n", ErrLive, ""},
		{"sample-aes", "#EXTM3U\n#EXT-X-KEY:METHOD=SAMPLE-AES,URI=\"k\"\n#EXTINF:4,\ns.ts\n#EXT-X-ENDLIST\n", ErrUnsupported, "SAMPLE-AES"},
		{"keyformat", "#EXTM3U\n#EXT-X-KEY:METHOD=AES-128,URI=\"k\",KEYFORMAT=\"com.apple.streamingkeydelivery\"\n", ErrUnsupported, ""},
		{"encrypted map without IV", "#EXTM3U\n#EXT-X-KEY:METHOD=AES-128,URI=\"k\"\n#EXT-X-MAP:URI=\"i.mp4\"\n", ErrUnsupported, ""},
		{"malformed EXTINF", "#EXTM3U\n#EXTINF:abc,\ns.ts\n#EXT-X-ENDLIST\n", nil, "line 2: malformed EXTINF"},
		{"segment without EXTINF", "#EXTM3U\ns.ts\n#EXT-X-ENDLIST\n", nil, "has no EXTINF"},
		{"implicit offset without predecessor", "#EXTM3U\n#EXTINF:4,\n#EXT-X-BYTERANGE:10\ns.ts\n#EXT-X-ENDLIST\n", nil, "without offset"},
		{"bad IV", "#EXTM3U\n#EXT-X-KEY:METHOD=AES-128,URI=\"k\",IV=0xzz\n", nil, "malformed IV"},
		{"not a playlist", "<html>", nil, "missing #EXTM3U"},
		{"empty", "#EXTM3U\n#EXT-X-ENDLIST\n", nil, "no segments"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := parsePlaylist([]byte(tt.body), mustURL(t, "http://h/p.m3u8"))
			if err == nil {
				t.Fatal("expected an error")
			}
			if tt.is != nil && !errors.Is(err, tt.is) {
				t.Fatalf("err = %v, want %v", err, tt.is)
			}
			if !strings.Contains(err.Error(), tt.contains) {
				t.Fatalf("err = %v, want it to mention %q", err, tt.contains)
			}
		})
	}
}

func TestParseAttrs(t *testing.T) {
	got := parseAttrs(`BANDWIDTH=1,CODECS="a, b",URI="x=y" , IV=0x1`)
	want := map[string]string{"BANDWIDTH": "1", "CODECS": "a, b", "URI": "x=y", "IV": "0x1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
}

func TestDetect(t *testing.T) {
	tests := []struct {
		ct, url, head string
		want          bool
	}{
		{"application/vnd.apple.mpegurl", "http://h/x", "", true},
		{"Application/X-MpegURL; charset=utf-8", "http://h/x", "", true},
		{"audio/mpegurl", "http://h/x", "", false}, // plain M3U radio
		{"application/octet-stream", "http://h/Live/Index.M3U8?tok=1", "", true},
		{"text/plain", "http://h/x.txt", "#EXTM3U\n", true},
		{"text/plain", "http://h/x.txt", "\xef\xbb\xbf#EXTM3U\n", true},
		{"video/mp2t", "http://h/x.ts", "G@", false},
	}
	for _, tt := range tests {
		if got := Detect(tt.ct, tt.url, []byte(tt.head)); got != tt.want {
			t.Errorf("Detect(%q, %q, %q) = %v", tt.ct, tt.url, tt.head, got)
		}
	}
}
