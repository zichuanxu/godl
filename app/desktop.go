package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/services/notifications"
	"github.com/zichuanxu/godl/internal/curlimport"
	"github.com/zichuanxu/godl/internal/download"
	"github.com/zichuanxu/godl/internal/logging"
	"github.com/zichuanxu/godl/internal/manager"
	"github.com/zichuanxu/godl/internal/service"
	"github.com/zichuanxu/godl/internal/settings"
)

// Event names emitted to the frontend.
const (
	eventDownload  = "download"  // download.Event
	eventResync    = "resync"    // events were dropped; reload the list
	eventClipboard = "clipboard" // a copied URL or curl command
)

func init() {
	application.RegisterEvent[download.Event](eventDownload)
	application.RegisterEvent[string](eventClipboard)
}

var errNotRunning = errors.New("the download service is not running")

// Desktop is bound to the frontend: its exported methods become TypeScript
// functions.
type Desktop struct {
	app      *application.App
	window   *application.WebviewWindow
	notifier *notifications.NotificationService

	svc      *service.Service
	mgr      *manager.Manager
	log      *slog.Logger
	logFile  io.Closer
	startErr error
	cancel   context.CancelFunc

	events chan download.Event
	lagged atomic.Bool
	// authorize asks once for permission to notify, on first use.
	authorize sync.Once
}

// State describes the backend to the frontend.
type State struct {
	Version string `json:"version"`
	// Error is set when the service failed to start, for example because a
	// headless "godl service" already holds the loopback port.
	Error            string `json:"error,omitempty"`
	DefaultDirectory string `json:"defaultDirectory"`
	Autostart        bool   `json:"autostart"`
}

// ServiceStartup starts the embedded download service before the window
// opens. A failure is reported through State instead of aborting, so the
// window can explain it.
func (d *Desktop) ServiceStartup(ctx context.Context, _ application.ServiceOptions) error {
	ctx, d.cancel = context.WithCancel(context.Background())
	d.log, d.logFile = openLog()
	d.events = make(chan download.Event, 1024)
	svc, err := service.New(service.Config{Logger: d.log, Publish: d.publish})
	if err == nil {
		err = svc.Start(ctx)
		if err != nil {
			_ = svc.Close()
		}
	}
	if err != nil {
		d.startErr = err
		d.log.Error("start service", "err", err)
		return nil
	}
	d.svc, d.mgr = svc, svc.Manager()
	go d.forward()
	go d.watchClipboard(ctx)
	return nil
}

// ServiceShutdown lets running downloads checkpoint before the process exits.
func (d *Desktop) ServiceShutdown() error {
	d.cancel()
	var err error
	if d.svc != nil {
		err = d.svc.Close()
	}
	if d.logFile != nil {
		_ = d.logFile.Close()
	}
	return err
}

func (d *Desktop) State() State {
	s := State{Version: version}
	if d.startErr != nil {
		s.Error = d.startErr.Error()
	}
	if root, err := service.DefaultDownloadRoot(); err == nil {
		s.DefaultDirectory = root
	}
	if d.app != nil {
		s.Autostart, _ = d.app.Autostart.IsEnabled()
	}
	return s
}

func (d *Desktop) manager() (*manager.Manager, error) {
	if d.mgr == nil {
		return nil, errNotRunning
	}
	return d.mgr, nil
}

func (d *Desktop) List() ([]download.Item, error) {
	m, err := d.manager()
	if err != nil {
		return nil, err
	}
	return m.List(context.Background())
}

func (d *Desktop) Add(req download.Request) (download.Item, error) {
	m, err := d.manager()
	if err != nil {
		return download.Item{}, err
	}
	return m.Add(context.Background(), req)
}

func (d *Desktop) Pause(id string) error  { return d.do(func(m *manager.Manager) error { return m.Pause(context.Background(), id) }) }
func (d *Desktop) Resume(id string) error { return d.do(func(m *manager.Manager) error { return m.Resume(context.Background(), id) }) }
func (d *Desktop) Retry(id string) error  { return d.do(func(m *manager.Manager) error { return m.Retry(context.Background(), id) }) }

func (d *Desktop) Delete(id string, removeFiles bool) error {
	return d.do(func(m *manager.Manager) error { return m.Delete(context.Background(), id, removeFiles) })
}

func (d *Desktop) Update(id string, patch download.Patch) error {
	return d.do(func(m *manager.Manager) error { return m.Update(context.Background(), id, patch) })
}

func (d *Desktop) do(fn func(*manager.Manager) error) error {
	m, err := d.manager()
	if err != nil {
		return err
	}
	return fn(m)
}

// PauseAll pauses every queued or running download.
func (d *Desktop) PauseAll() error {
	return d.each(func(m *manager.Manager, item download.Item) error {
		if item.Status == download.StatusQueued || item.Status == download.StatusRunning {
			return m.Pause(context.Background(), item.ID)
		}
		return nil
	})
}

// ResumeAll requeues every paused download.
func (d *Desktop) ResumeAll() error {
	return d.each(func(m *manager.Manager, item download.Item) error {
		if item.Status == download.StatusPaused {
			return m.Resume(context.Background(), item.ID)
		}
		return nil
	})
}

func (d *Desktop) each(fn func(*manager.Manager, download.Item) error) error {
	m, err := d.manager()
	if err != nil {
		return err
	}
	items, err := m.List(context.Background())
	if err != nil {
		return err
	}
	var errs []error
	for _, item := range items {
		errs = append(errs, fn(m, item))
	}
	return errors.Join(errs...)
}

func (d *Desktop) Settings() (settings.Settings, error) {
	m, err := d.manager()
	if err != nil {
		return settings.Settings{}, err
	}
	return m.Settings().Masked(), nil
}

func (d *Desktop) SaveSettings(s settings.Settings) (settings.Settings, error) {
	m, err := d.manager()
	if err != nil {
		return settings.Settings{}, err
	}
	if err := m.UpdateSettings(context.Background(), s); err != nil {
		return settings.Settings{}, err
	}
	return m.Settings().Masked(), nil
}

// SetAutostart registers or removes the app as a login item.
func (d *Desktop) SetAutostart(enabled bool) error {
	if enabled {
		return d.app.Autostart.Enable()
	}
	return d.app.Autostart.Disable()
}

// ParseCurl reads a browser's "Copy as cURL" command into a request.
func (d *Desktop) ParseCurl(command string) (download.Request, error) {
	r, err := curlimport.Parse(command)
	if err != nil {
		return download.Request{}, err
	}
	return download.Request{URL: r.URL, Headers: r.Headers}, nil
}

// Reauthenticate replaces a download's request headers with those of a fresh
// curl command and retries it; used after a 401 or 403.
func (d *Desktop) Reauthenticate(id, command string) error {
	r, err := curlimport.Parse(command)
	if err != nil {
		return err
	}
	return d.do(func(m *manager.Manager) error {
		if err := m.Update(context.Background(), id, download.Patch{Headers: &r.Headers}); err != nil {
			return err
		}
		err := m.Retry(context.Background(), id)
		if errors.Is(err, download.ErrInvalidTransition) {
			return nil // not failed: the new headers apply from the next start
		}
		return err
	})
}

// PickDirectory asks for a folder and allows downloads into it.
func (d *Desktop) PickDirectory() (string, error) {
	dir, err := d.app.Dialog.OpenFile().
		SetTitle("Choose a download folder").
		CanChooseDirectories(true).
		CanChooseFiles(false).
		CanCreateDirectories(true).
		PromptForSingleSelection()
	if err != nil || dir == "" {
		return "", err
	}
	m, err := d.manager()
	if err != nil {
		return "", err
	}
	s := m.Settings()
	for _, existing := range s.ExtraRoots {
		if existing == dir {
			return dir, nil
		}
	}
	s.ExtraRoots = append(s.ExtraRoots, dir)
	if err := m.UpdateSettings(context.Background(), s); err != nil {
		return "", err
	}
	return dir, nil
}

// Open opens a completed download with its default application.
func (d *Desktop) Open(id string) error {
	item, err := d.item(id)
	if err != nil {
		return err
	}
	if item.Status != download.StatusCompleted {
		return errors.New("the download has not completed")
	}
	return d.app.Browser.OpenFile(item.Destination)
}

// Reveal shows a download in Finder or Explorer, or its folder while the
// file does not exist yet.
func (d *Desktop) Reveal(id string) error {
	item, err := d.item(id)
	if err != nil {
		return err
	}
	if _, err := os.Stat(item.Destination); err == nil {
		return d.app.Env.OpenFileManager(item.Destination, true)
	}
	return d.app.Env.OpenFileManager(filepath.Dir(item.Destination), false)
}

func (d *Desktop) item(id string) (download.Item, error) {
	items, err := d.List()
	if err != nil {
		return download.Item{}, err
	}
	for _, item := range items {
		if item.ID == id {
			return item, nil
		}
	}
	return download.Item{}, download.ErrNotFound
}

func (d *Desktop) show() {
	if d.window != nil {
		d.window.Show().Focus()
	}
}

// publish receives manager events, possibly under the manager's lock, so it
// never blocks: when the frontend falls behind it is told to reload.
func (d *Desktop) publish(e download.Event) {
	select {
	case d.events <- e:
	default:
		d.lagged.Store(true)
	}
}

// forward relays events to the frontend and notifies on completion.
func (d *Desktop) forward() {
	running := map[string]bool{}
	for e := range d.events {
		if d.lagged.Swap(false) {
			d.app.Event.Emit(eventResync)
		}
		d.app.Event.Emit(eventDownload, e)
		item := e.Item
		switch {
		case e.Type == download.EventDeleted:
			delete(running, item.ID)
		case item.Status == download.StatusRunning:
			running[item.ID] = true
		case running[item.ID] && e.Type == download.EventUpdated:
			delete(running, item.ID)
			d.notify(item)
		}
	}
}

func (d *Desktop) notify(item download.Item) {
	if d.notifier == nil || !d.mgr.Settings().Desktop.Notifications {
		return
	}
	var title string
	switch item.Status {
	case download.StatusCompleted:
		title = "Download complete"
	case download.StatusFailed:
		title = "Download failed"
	default:
		return
	}
	d.authorize.Do(func() {
		if ok, err := d.notifier.RequestNotificationAuthorization(); !ok || err != nil {
			d.log.Warn("notifications not authorized", "err", err)
		}
	})
	err := d.notifier.SendNotification(notifications.NotificationOptions{
		ID: item.ID + "-" + string(item.Status), Title: title, Body: filepath.Base(item.Destination),
	})
	if err != nil {
		d.log.Warn("send notification", "err", err)
	}
}

// watchClipboard offers copied URLs and curl commands to the frontend. The
// clipboard's content at startup is never offered.
func (d *Desktop) watchClipboard(ctx context.Context) {
	last, _ := d.app.Clipboard.Text()
	ticker := time.NewTicker(1500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if !d.mgr.Settings().Desktop.ClipboardMonitor {
			continue
		}
		text, ok := d.app.Clipboard.Text()
		if !ok || text == last {
			continue
		}
		last = text
		if offerable(text) {
			d.app.Event.Emit(eventClipboard, strings.TrimSpace(text))
		}
	}
}

// offerable reports a single http(s) URL or a curl command.
func offerable(text string) bool {
	text = strings.TrimSpace(text)
	if len(text) > 64<<10 {
		return false
	}
	if curlimport.Looks(text) {
		return true
	}
	if strings.ContainsAny(text, " \n\t") {
		return false
	}
	u, err := url.Parse(text)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

// openLog writes JSON logs to the data directory, keeping one previous file
// once the current one passes 10 MB (DESIGN.md section 7).
func openLog() (*slog.Logger, io.Closer) {
	dir, err := service.DataDir()
	if err != nil {
		return logging.New(os.Stderr, slog.LevelInfo), io.NopCloser(nil)
	}
	path := filepath.Join(dir, "godl.log")
	if fi, err := os.Stat(path); err == nil && fi.Size() > 10<<20 {
		_ = os.Rename(path, path+".1")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return logging.New(os.Stderr, slog.LevelInfo), io.NopCloser(nil)
	}
	fmt.Fprintln(f) // separates runs
	return logging.New(f, slog.LevelInfo), f
}
