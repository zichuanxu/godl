// Command godl-desktop is the godl desktop app: the download service runs in
// this process, the React UI talks to it through Wails bindings and events,
// and the loopback API stays up for the CLI (DESIGN.md section 1).
package main

import (
	"embed"
	"log"
	"runtime"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

//go:embed all:frontend/dist
var assets embed.FS

var (
	//go:embed icons/tray-template.png
	trayTemplate []byte // macOS menu bar glyph, tinted by the system
	//go:embed icons/tray-windows.png
	trayIcon []byte
)

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
		Name:        "godl",
		Description: "A fast download manager",
		Services:    services,
		Assets:      application.AssetOptions{Handler: application.AssetFileServerFS(assets)},
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: false, // keep downloading from the tray
		},
		SingleInstance: &application.SingleInstanceOptions{
			UniqueID:               "io.github.zichuanxu.godl",
			OnSecondInstanceLaunch: func(application.SecondInstanceData) { desk.show() },
		},
	})
	desk.app = app

	win := app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name: "main", Title: "godl", Width: 1100, Height: 700, MinWidth: 720, MinHeight: 440, URL: "/",
	})
	desk.window = win
	// Closing the window hides it; downloads continue until Quit.
	win.RegisterHook(events.Common.WindowClosing, func(e *application.WindowEvent) {
		win.Hide()
		e.Cancel()
	})

	tray := app.SystemTray.New()
	if runtime.GOOS == "darwin" {
		tray.SetTemplateIcon(trayTemplate)
	} else {
		tray.SetIcon(trayIcon)
	}
	tray.SetTooltip("godl")
	menu := app.NewMenu()
	menu.Add("Show godl").OnClick(func(*application.Context) { desk.show() })
	menu.AddSeparator()
	menu.Add("Pause all").OnClick(func(*application.Context) { desk.PauseAll() })
	menu.Add("Resume all").OnClick(func(*application.Context) { desk.ResumeAll() })
	menu.AddSeparator()
	menu.Add("Quit godl").OnClick(func(*application.Context) { app.Quit() })
	tray.SetMenu(menu)
	tray.OnClick(desk.show)
	app.Event.OnApplicationEvent(events.Mac.ApplicationShouldHandleReopen, func(*application.ApplicationEvent) { desk.show() })

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}
