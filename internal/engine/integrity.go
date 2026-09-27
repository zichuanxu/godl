package engine

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

func validateSegmentResponse(resp *http.Response, start, end, total int64, expectedETag string) error {
	if resp.StatusCode != http.StatusPartialContent {
		return fmt.Errorf("segment returned HTTP %s", resp.Status)
	}
	if encoding := strings.TrimSpace(resp.Header.Get("Content-Encoding")); encoding != "" && !strings.EqualFold(encoding, "identity") {
		return fmt.Errorf("segment returned unsupported Content-Encoding %q", encoding)
	}
	if got := strongETag(resp.Header.Get("ETag")); got != expectedETag {
		return errors.New("segment ETag changed")
	}
	gotStart, gotEnd, gotTotal, ok := parseContentRange(resp.Header.Get("Content-Range"))
	if !ok || gotStart != start || gotEnd != end || gotTotal != total {
		return fmt.Errorf("unexpected Content-Range %q", resp.Header.Get("Content-Range"))
	}
	length := end - start + 1
	if resp.ContentLength >= 0 && resp.ContentLength != length {
		return fmt.Errorf("segment Content-Length %d; want %d", resp.ContentLength, length)
	}
	return nil
}

func readExactResponse(r io.Reader, length int64) ([]byte, error) {
	if length < 0 || length > int64(^uint(0)>>1) {
		return nil, errors.New("invalid response length")
	}
	data := make([]byte, int(length))
	if _, err := io.ReadFull(r, data); err != nil {
		return nil, err
	}
	var one [1]byte
	if n, err := r.Read(one[:]); n > 0 {
		return nil, errors.New("response contains more bytes than declared")
	} else if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	return data, nil
}

func parseContentLength(value string) (int64, error) {
	if value == "" {
		return -1, nil
	}
	return strconv.ParseInt(value, 10, 64)
}
