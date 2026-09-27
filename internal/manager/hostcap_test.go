package manager

import "testing"

func TestHostCapsTrackReservations(t *testing.T) {
	caps := newHostCaps(2)
	if !caps.Acquire("example.com") || !caps.Acquire("example.com") {
		t.Fatal("first two reservations should succeed")
	}
	if caps.Acquire("example.com") {
		t.Fatal("third reservation exceeded host cap")
	}
	if !caps.Acquire("other.example") {
		t.Fatal("different host should have independent cap")
	}
	caps.Release("example.com")
	if !caps.Acquire("example.com") {
		t.Fatal("released reservation was not reusable")
	}
}
