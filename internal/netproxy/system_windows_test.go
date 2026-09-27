//go:build windows

package netproxy

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"golang.org/x/sys/windows/registry"
)

func TestReadSystemWindows(t *testing.T) {
	k, err := registry.OpenKey(registry.CURRENT_USER, internetSettingsKey, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()

	// Save the current values and restore them (or delete them) afterwards.
	origEnable, _, enableErr := k.GetIntegerValue("ProxyEnable")
	origServer, _, serverErr := k.GetStringValue("ProxyServer")
	origOverride, _, overrideErr := k.GetStringValue("ProxyOverride")
	for _, err := range []error{enableErr, serverErr, overrideErr} {
		if err != nil && !errors.Is(err, registry.ErrNotExist) {
			t.Fatalf("reading original values: %v", err)
		}
	}
	t.Cleanup(func() {
		k, err := registry.OpenKey(registry.CURRENT_USER, internetSettingsKey, registry.SET_VALUE)
		if err != nil {
			t.Errorf("restore: %v", err)
			return
		}
		defer k.Close()
		restore := func(name string, existed bool, set func() error) {
			if existed {
				err = set()
			} else {
				err = k.DeleteValue(name)
			}
			if err != nil {
				t.Errorf("restore %s: %v", name, err)
			}
		}
		restore("ProxyEnable", enableErr == nil, func() error { return k.SetDWordValue("ProxyEnable", uint32(origEnable)) })
		restore("ProxyServer", serverErr == nil, func() error { return k.SetStringValue("ProxyServer", origServer) })
		restore("ProxyOverride", overrideErr == nil, func() error { return k.SetStringValue("ProxyOverride", origOverride) })

		sys.mu.Lock()
		sys.s, sys.at = nil, time.Time{}
		sys.mu.Unlock()
	})

	for name, set := range map[string]func() error{
		"ProxyEnable":   func() error { return k.SetDWordValue("ProxyEnable", 1) },
		"ProxyServer":   func() error { return k.SetStringValue("ProxyServer", "http=127.0.0.2:3128;https=127.0.0.2:3129") },
		"ProxyOverride": func() error { return k.SetStringValue("ProxyOverride", "<local>;*.internal.test") },
	} {
		if err := set(); err != nil {
			t.Fatalf("set %s: %v", name, err)
		}
	}

	sys.mu.Lock()
	sys.s, sys.at = nil, time.Time{} // force a fresh read
	sys.mu.Unlock()

	f := Func(func() Config { return Config{Mode: ModeSystem} })
	for target, want := range map[string]string{
		"http://example.com/":      "http://127.0.0.2:3128",
		"https://example.com/":     "http://127.0.0.2:3129",
		"http://intranet/":         "",
		"https://a.internal.test/": "",
	} {
		req, _ := http.NewRequest(http.MethodGet, target, nil)
		u, err := f(req)
		if err != nil {
			t.Fatalf("%s: %v", target, err)
		}
		got := ""
		if u != nil {
			got = u.String()
		}
		if got != want {
			t.Errorf("%s: got %q, want %q", target, got, want)
		}
	}
}
