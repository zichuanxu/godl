package engine

import (
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"strings"
)

// ErrChecksumMismatch reports a completed file whose digest differs from the
// expected one. The partial file is discarded.
var ErrChecksumMismatch = errors.New("checksum mismatch")

type checksum struct {
	algo string
	want []byte
	new  func() hash.Hash
}

var checksumAlgorithms = map[string]func() hash.Hash{
	"sha256": sha256.New,
	"sha512": sha512.New,
	"sha1":   sha1.New,
	"md5":    md5.New,
}

// parseChecksum accepts "algo:hex", for example "sha256:9f86d0...". An empty
// string means no verification.
func parseChecksum(value string) (*checksum, error) {
	if value == "" {
		return nil, nil
	}
	algo, digest, ok := strings.Cut(value, ":")
	algo = strings.ToLower(strings.TrimSpace(algo))
	newHash, known := checksumAlgorithms[algo]
	if !ok || !known {
		return nil, fmt.Errorf("checksum must be algo:hex with algo one of sha256, sha512, sha1, md5; got %q", value)
	}
	want, err := hex.DecodeString(strings.TrimSpace(digest))
	if err != nil || len(want) != newHash().Size() {
		return nil, fmt.Errorf("invalid %s digest %q", algo, digest)
	}
	return &checksum{algo: algo, want: want, new: newHash}, nil
}

func (c *checksum) verify(path string) error {
	if c == nil {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open file for %s: %w", c.algo, err)
	}
	defer f.Close()
	h := c.new()
	if _, err := io.Copy(h, f); err != nil {
		return fmt.Errorf("calculate %s: %w", c.algo, err)
	}
	if got := h.Sum(nil); subtle.ConstantTimeCompare(got, c.want) != 1 {
		return fmt.Errorf("%w: %s got %x, want %x", ErrChecksumMismatch, c.algo, got, c.want)
	}
	return nil
}
