// Command nimget-desktop is the NimGet desktop app: the download service runs in
// this process, the React UI talks to it through Wails bindings and events,
// and the loopback API stays up for the CLI (DESIGN.md section 1).
package main

import (
	"embed"
	"log"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

//go:embed all:frontend/dist
var assets embed.FS

//go:embed icons/tray.png
var trayIcon []byte

// Set at link time: -X main.version=...
var version = "dev"

func main() {
	desk := &Desktop{}
	services := []application.Service{application.NewService(desk)}
	if notifier := newNotifier(); notifier != nil {
		desk.notifier.Store(notifier)
		services = append(services, application.NewService(&optionalNotifier{NotificationService: notifier, desk: desk}))
	}
	app := application.New(application.Options{
		Name:        "NimGet",
		Description: "A lightweight, open-source download manager",
		Services:    services,
		Assets:      application.AssetOptions{Handler: application.AssetFileServerFS(assets)},
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: false, // keep downloading from the tray
		},
		SingleInstance: &application.SingleInstanceOptions{
			UniqueID:               "io.github.zichuanxu.nimget",
			OnSecondInstanceLaunch: func(application.SecondInstanceData) { desk.show() },
		},
	})
	desk.app = app

	win := app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name: "main", Title: "NimGet", Width: 1120, Height: 720, MinWidth: 820, MinHeight: 500, URL: "/",
		// Hidden inset title bar over a translucent sidebar; the frontend
		// marks its header as the drag region.
		Mac: application.MacWindow{
			TitleBar: application.MacTitleBarHiddenInset,
			Backdrop: application.MacBackdropTranslucent,
		},
	})
	desk.window = win
	// Closing the window hides it; downloads continue until Quit.
	win.RegisterHook(events.Common.WindowClosing, func(e *application.WindowEvent) {
		win.Hide()
		e.Cancel()
	})

	tray := app.SystemTray.New()
	tray.SetIcon(trayIcon)
	tray.SetTooltip("NimGet")
	menu := app.NewMenu()
	item := func(label string, fn func()) {
		mi := menu.Add(label).OnClick(func(*application.Context) { fn() })
		desk.tray = append(desk.tray, trayItem{item: mi, label: label})
	}
	item("Show NimGet", desk.show)
	menu.AddSeparator()
	item("Pause all", func() { _ = desk.PauseAll() })
	item("Resume all", func() { _ = desk.ResumeAll() })
	menu.AddSeparator()
	item("Quit NimGet", app.Quit)
	tray.SetMenu(menu)
	tray.OnClick(desk.show)
	app.Event.OnApplicationEvent(events.Mac.ApplicationShouldHandleReopen, func(*application.ApplicationEvent) { desk.show() })

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}
