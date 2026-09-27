package app

import "testing"

func TestDefaultMenuContainsServiceActions(t *testing.T) {
	menu := DefaultMenu()
	for _, id := range []string{"show", "add", "pause-all", "settings", "quit"} {
		if !menu.Contains(id) {
			t.Fatalf("menu does not contain %q", id)
		}
	}
}
