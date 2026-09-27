// Package queuefile exports the queue to a portable JSON file and imports it,
// or a plain list of URLs, back.
package queuefile

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/zichuanxu/nimget/internal/download"
)

const version = 1

type file struct {
	Version   int                `json:"version"`
	Downloads []download.Request `json:"downloads"`
}

// Export writes the downloads that are not completed. Request headers are
// left out unless withHeaders is set: they often hold session cookies.
func Export(items []download.Item, withHeaders bool) ([]byte, error) {
	f := file{Version: version, Downloads: []download.Request{}}
	for _, item := range items {
		if item.Status == download.StatusCompleted {
			continue
		}
		req := download.Request{
			URL: item.URL, Destination: item.Destination, Priority: item.Priority,
			Connections: item.Connections, SpeedLimit: item.SpeedLimit, Checksum: item.Checksum,
		}
		if withHeaders {
			req.Headers = item.Headers
		}
		f.Downloads = append(f.Downloads, req)
	}
	return json.MarshalIndent(f, "", "  ")
}

// Import reads an exported file, or text with one URL per line ("#" starts
// a comment).
func Import(data []byte) ([]download.Request, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) > 0 && trimmed[0] == '{' {
		var f file
		dec := json.NewDecoder(bytes.NewReader(trimmed))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&f); err != nil {
			return nil, fmt.Errorf("read queue file: %w", err)
		}
		if f.Version != version {
			return nil, fmt.Errorf("unsupported queue file version %d", f.Version)
		}
		return f.Downloads, nil
	}
	var reqs []download.Request
	scanner := bufio.NewScanner(bytes.NewReader(trimmed))
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		reqs = append(reqs, download.Request{URL: line})
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(reqs) == 0 {
		return nil, errors.New("no downloads found")
	}
	return reqs, nil
}
