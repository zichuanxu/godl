package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/services/notifications"
	"github.com/zichuanxu/godl/internal/curlimport"
	"github.com/zichuanxu/godl/internal/download"
	"github.com/zichuanxu/godl/internal/logging"
	"github.com/zichuanxu/godl/internal/manager"
	"github.com/zichuanxu/godl/internal/netproxy"
	"github.com/zichuanxu/godl/internal/service"
	"github.com/zichuanxu/godl/internal/settings"
	"github.com/zichuanxu/godl/internal/update"
)

// Event names emitted to the frontend.
const (
	eventDownload  = "download"  // download.Event
	eventResync    = "resync"    // events were dropped; reload the list
	eventClipboard = "clipboard" // a copied URL or curl command
	eventStopped   = "stopped"   // the service stopped; re-read State
	eventUpdate    = "update"    // update.Release: a newer version exists
)

func init() {
	application.RegisterEvent[download.Event](eventDownload)
	application.RegisterEvent[string](eventClipboard)
	application.RegisterEvent[update.Release](eventUpdate)
}

var errNotRunning = errors.New("the download service is not running")

// Desktop is bound to the frontend: its exported methods become TypeScript
// functions.
type Desktop struct {
	app      *application.App
	window   *application.WebviewWindow
	notifier atomic.Pointer[notifications.NotificationService]

	svc     *service.Service
	mgr     *manager.Manager
	log     *slog.Logger
	logFile io.Closer
	// failure holds why the service is not running, if it is not.
	failure atomic.Value
	cancel  context.CancelFunc

	events chan download.Event
	lagged atomic.Bool
	// notifications are sent from their own goroutine: a pending permission
	// prompt must not hold up the event stream.
	notifications chan download.Item
	// whenDone is the pending completion action (a string).
	whenDone atomic.Value
	// newer is the newest release, when it is newer than this build.
	newer atomic.Pointer[update.Release]
}

// State describes the backend to the frontend.
type State struct {
	Version string `json:"version"`
	// Error is set when the service failed to start, for example because a
	// headless "godl service" already holds the loopback port.
	Error            string `json:"error,omitempty"`
	DefaultDirectory string `json:"defaultDirectory"`
	Autostart        bool   `json:"autostart"`
	// Update is set when a newer release is available.
	Update *update.Release `json:"update,omitempty"`
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
		d.failure.Store(err.Error())
		d.log.Error("start service", "err", err)
		return nil
	}
	d.svc, d.mgr = svc, svc.Manager()
	d.notifications = make(chan download.Item, 64)
	go d.forward()
	go d.notifyLoop()
	go d.watchClipboard(ctx)
	go d.watchReleases(ctx)
	go func() {
		err := svc.Wait()
		if ctx.Err() != nil {
			return // our own shutdown
		}
		msg := "the service stopped unexpectedly"
		if err != nil {
			msg = err.Error()
		}
		d.failure.Store(msg)
		d.log.Error("service stopped", "err", err)
		d.app.Event.Emit(eventStopped)
	}()
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
	s.Error, _ = d.failure.Load().(string)
	if root, err := service.DefaultDownloadRoot(); err == nil {
		s.DefaultDirectory = root
	}
	if d.app != nil {
		s.Autostart, _ = d.app.Autostart.IsEnabled()
	}
	if d.mgr != nil && d.mgr.Settings().Desktop.CheckUpdates {
		s.Update = d.newer.Load()
	}
	return s
}

func (d *Desktop) manager() (*manager.Manager, error) {
	if d.mgr == nil || d.failure.Load() != nil {
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

func (d *Desktop) Pause(id string) error {
	return d.do(func(m *manager.Manager) error { return m.Pause(context.Background(), id) })
}
func (d *Desktop) Resume(id string) error {
	return d.do(func(m *manager.Manager) error { return m.Resume(context.Background(), id) })
}
func (d *Desktop) Retry(id string) error {
	return d.do(func(m *manager.Manager) error { return m.Retry(context.Background(), id) })
}

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
// curl command for the same host and retries it; used after a 401 or 403.
func (d *Desktop) Reauthenticate(id, command string) error {
	r, err := curlimport.Parse(command)
	if err != nil {
		return err
	}
	item, err := d.item(id)
	if err != nil {
		return err
	}
	if host(r.URL) != host(item.URL) {
		return fmt.Errorf("the command is for %s, but the download is from %s", host(r.URL), host(item.URL))
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
	m, err := d.manager()
	if err != nil {
		return "", err
	}
	dir, err := d.app.Dialog.OpenFile().
		SetTitle("Choose a download folder").
		CanChooseDirectories(true).
		CanChooseFiles(false).
		CanCreateDirectories(true).
		PromptForSingleSelection()
	if err != nil || dir == "" {
		return "", err
	}
	return dir, m.AddExtraRoot(context.Background(), dir)
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
		d.app.Event.Emit(eventDownload, e)
		// Reload only once the backlog is out, so no stale event lands after
		// the fresh list.
		if len(d.events) == 0 && d.lagged.Swap(false) {
			d.app.Event.Emit(eventResync)
		}
		item := e.Item
		switch {
		case e.Type == download.EventDeleted:
			delete(running, item.ID)
		case item.Status == download.StatusRunning:
			running[item.ID] = true
		case running[item.ID] && e.Type == download.EventUpdated:
			delete(running, item.ID)
			select {
			case d.notifications <- item:
			default: // a burst of completions; skip the extra banners
			}
			d.afterStop(item)
		}
	}
}

// notifyLoop announces finished and failed downloads.
func (d *Desktop) notifyLoop() {
	authorized := false
	for item := range d.notifications {
		notifier := d.notifier.Load()
		if notifier == nil || !d.mgr.Settings().Desktop.Notifications {
			continue
		}
		var title string
		switch item.Status {
		case download.StatusCompleted:
			title = "Download complete"
		case download.StatusFailed:
			title = "Download failed"
		default:
			continue
		}
		if !authorized {
			// On macOS this waits for the user to answer the permission prompt.
			if ok, err := notifier.RequestNotificationAuthorization(); !ok || err != nil {
				d.log.Warn("notifications not authorized", "err", err)
				continue
			}
			authorized = true
		}
		err := notifier.SendNotification(notifications.NotificationOptions{
			ID: item.ID + "-" + string(item.Status), Title: title, Body: filepath.Base(item.Destination),
		})
		if err != nil {
			d.log.Warn("send notification", "err", err)
		}
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
		text, ok := d.app.Clipboard.Text() // Wails reads it on the main thread
		if !ok || text == last {
			continue
		}
		last = text // also while disabled: turning the monitor on offers nothing old
		if d.mgr.Settings().Desktop.ClipboardMonitor && offerable(text) {
			d.app.Event.Emit(eventClipboard, strings.TrimSpace(text))
		}
	}
}

func host(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

// watchReleases checks for a newer version shortly after startup and then
// every few hours; update.Check itself polls GitHub at most once a week.
func (d *Desktop) watchReleases(ctx context.Context) {
	dir, err := service.DataDir()
	if err != nil {
		return
	}
	// The check goes through the proxy configured in godl, like downloads.
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = netproxy.Func(func() netproxy.Config { return d.mgr.Settings().Proxy })
	checker := &update.Checker{
		Client: &http.Client{Transport: transport, Timeout: 20 * time.Second},
		URL:    update.LatestURL, Path: update.StatePath(dir),
	}
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		timer.Reset(6 * time.Hour)
		if !d.mgr.Settings().Desktop.CheckUpdates {
			continue
		}
		rel, ok, err := checker.Check(ctx, version, time.Now())
		if err != nil {
			d.log.Warn("update check", "err", err)
			continue
		}
		if ok {
			d.newer.Store(&rel)
			d.app.Event.Emit(eventUpdate, rel)
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

// openLog writes JSON logs to the data directory, rotating at 10 MB and
// keeping three older files (DESIGN.md section 7).
func openLog() (*slog.Logger, io.Closer) {
	dir, err := service.DataDir()
	if err != nil {
		return logging.New(os.Stderr, slog.LevelInfo), io.NopCloser(nil)
	}
	f, err := logging.OpenRotating(filepath.Join(dir, "godl.log"), 10<<20, 3)
	if err != nil {
		return logging.New(os.Stderr, slog.LevelInfo), io.NopCloser(nil)
	}
	return logging.New(f, slog.LevelInfo), f
}
