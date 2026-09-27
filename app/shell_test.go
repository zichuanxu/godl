package app

import "testing"

func TestDesktopShellDefaults(t *testing.T) {
	config := DefaultWindowConfig()
	if config.Title != "godl" || config.Width != 1180 || config.Height != 760 {
		t.Fatalf("window config = %+v", config)
	}
	if config.MinWidth < 720 || config.MinHeight < 480 {
		t.Fatalf("window minimum = %dx%d", config.MinWidth, config.MinHeight)
	}
}
