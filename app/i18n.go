package main

import (
	"strings"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// zhCN translates the few strings the Go side shows: the tray menu,
// notifications, and dialog titles. The frontend has its own catalogue.
var zhCN = map[string]string{
	"Show godl":                "显示 godl",
	"Pause all":                "全部暂停",
	"Resume all":               "全部继续",
	"Quit godl":                "退出 godl",
	"Download complete":        "下载完成",
	"Download failed":          "下载失败",
	"Choose a download folder": "选择下载文件夹",
}

// normalizeLocale maps a BCP 47 tag to a shipped language.
func normalizeLocale(tag string) string {
	if strings.HasPrefix(strings.ToLower(tag), "zh") {
		return "zh-CN"
	}
	return "en"
}

func (d *Desktop) tr(s string) string {
	if lang, _ := d.locale.Load().(string); lang == "zh-CN" {
		if t, ok := zhCN[s]; ok {
			return t
		}
	}
	return s
}

// trayItem is a tray menu entry relabelled when the language changes.
type trayItem struct {
	item  *application.MenuItem
	label string
}

// SetLocale tells the backend the language the frontend resolved, so the tray
// menu and notifications match it.
func (d *Desktop) SetLocale(tag string) {
	d.locale.Store(normalizeLocale(tag))
	for _, t := range d.tray {
		t.item.SetLabel(d.tr(t.label))
	}
}
