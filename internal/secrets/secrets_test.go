package secrets

import (
	"errors"
	"strings"
	"testing"
)

func TestSealRoundTripAndForeignKey(t *testing.T) {
	a, err := New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := a.Seal(`{"Cookie":"session=abc"}`)
	if err != nil || !strings.HasPrefix(sealed, prefix) || strings.Contains(sealed, "session") {
		t.Fatalf("sealed = %q, %v", sealed, err)
	}
	if plain, err := a.Open(sealed); err != nil || plain != `{"Cookie":"session=abc"}` {
		t.Fatalf("open = %q, %v", plain, err)
	}
	other := make([]byte, 32)
	other[0] = 1
	b, _ := New(other)
	if _, err := b.Open(sealed); !errors.Is(err, ErrUnreadable) {
		t.Fatalf("foreign key opened the value: %v", err)
	}
	if plain, err := b.Open(`{"legacy":"plain"}`); err != nil || plain != `{"legacy":"plain"}` {
		t.Fatal("legacy plaintext not passed through")
	}
	if empty, _ := a.Seal(""); empty != "" {
		t.Fatal("empty value sealed")
	}
}
