package netproxy

import (
	"errors"

	"golang.org/x/sys/windows/registry"
)

const internetSettingsKey = `Software\Microsoft\Windows\CurrentVersion\Internet Settings`

// readSystem reads the per-user WinINet proxy settings. A nil result means no
// proxy is enabled.
func readSystem() (*settings, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, internetSettingsKey, registry.QUERY_VALUE)
	if err != nil {
		return nil, err
	}
	defer k.Close()
	enable, _, err := k.GetIntegerValue("ProxyEnable")
	if errors.Is(err, registry.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if enable == 0 {
		return nil, nil
	}
	server, _, err := k.GetStringValue("ProxyServer")
	if err != nil && !errors.Is(err, registry.ErrNotExist) {
		return nil, err
	}
	override, _, err := k.GetStringValue("ProxyOverride")
	if err != nil && !errors.Is(err, registry.ErrNotExist) {
		return nil, err
	}
	return parseWindows(server, override), nil
}
