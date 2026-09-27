// Package app contains the optional desktop shell configuration.
package app

type WindowConfig struct {
	Title                    string
	Width                    int
	Height                   int
	MinWidth                 int
	MinHeight                int
	HideWindowOnClose        bool
	EnableDefaultContextMenu bool
}

func DefaultWindowConfig() WindowConfig {
	return WindowConfig{
		Title:                    "godl",
		Width:                    1180,
		Height:                   760,
		MinWidth:                 720,
		MinHeight:                480,
		HideWindowOnClose:        true,
		EnableDefaultContextMenu: false,
	}
}
