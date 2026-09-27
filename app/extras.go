package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/zichuanxu/godl/internal/batch"
	"github.com/zichuanxu/godl/internal/download"
	"github.com/zichuanxu/godl/internal/power"
	"github.com/zichuanxu/godl/internal/queuefile"
	"github.com/zichuanxu/godl/internal/remux"
)

// eventQueueDone carries the action to run once the queue has emptied; the
// frontend shows a countdown and then calls PerformWhenDone.
const eventQueueDone = "queue-done"

func init() {
	application.RegisterEvent[string](eventQueueDone)
}

// CanConvert reports whether ffmpeg is available for ConvertToMP4.
func (d *Desktop) CanConvert() bool {
	m, err := d.manager()
	if err != nil {
		return false
	}
	_, err = remux.Find(m.Settings().Desktop.FFmpeg)
	return err == nil
}

// ConvertToMP4 remuxes a completed MPEG-TS download into an MP4 next to it,
// without re-encoding, and returns the new file's path.
func (d *Desktop) ConvertToMP4(id string) (string, error) {
	item, err := d.item(id)
	if err != nil {
		return "", err
	}
	if item.Status != download.StatusCompleted || !strings.EqualFold(filepath.Ext(item.Destination), ".ts") {
		return "", errors.New("only completed .ts downloads can be converted")
	}
	ffmpeg, err := remux.Find(d.mgr.Settings().Desktop.FFmpeg)
	if err != nil {
		return "", err
	}
	out, err := remux.ToMP4(context.Background(), ffmpeg, item.Destination)
	if err != nil {
		d.log.Warn("convert to MP4", "id", id, "err", err)
	}
	return out, err
}

// ExpandBatch returns the URLs a batch pattern such as img[001-120].jpg
// describes.
func (d *Desktop) ExpandBatch(pattern string) ([]string, error) {
	return batch.Expand(pattern)
}

// ExportQueue saves the unfinished downloads to a file the user picks. Request
// headers are left out; they often hold session cookies. It returns the
// path, or "" when the user cancels.
func (d *Desktop) ExportQueue() (string, error) {
	m, err := d.manager()
	if err != nil {
		return "", err
	}
	path, err := d.app.Dialog.SaveFile().SetFilename("nimget-queue.json").CanCreateDirectories(true).PromptForSingleSelection()
	if err != nil || path == "" {
		return "", err
	}
	items, err := m.List(context.Background())
	if err != nil {
		return "", err
	}
	data, err := queuefile.Export(items, false)
	if err != nil {
		return "", err
	}
	return path, os.WriteFile(path, append(data, '\n'), 0o600)
}

// ImportResult reports an import.
type ImportResult struct {
	Added  int      `json:"added"`
	Failed []string `json:"failed"`
}

// ImportQueue queues the downloads in an exported file or a list of URLs the
// user picks.
func (d *Desktop) ImportQueue() (ImportResult, error) {
	m, err := d.manager()
	if err != nil {
		return ImportResult{}, err
	}
	path, err := d.app.Dialog.OpenFile().CanChooseFiles(true).PromptForSingleSelection()
	if err != nil || path == "" {
		return ImportResult{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ImportResult{}, err
	}
	reqs, err := queuefile.Import(data)
	if err != nil {
		return ImportResult{}, err
	}
	res := ImportResult{Failed: []string{}}
	for _, req := range reqs {
		if _, err := m.Add(context.Background(), req); err != nil {
			res.Failed = append(res.Failed, req.URL+": "+err.Error())
			continue
		}
		res.Added++
	}
	return res, nil
}

// SetWhenDone chooses what happens once no download is queued or running:
// "" for nothing, "sleep", or "shutdown". It fires once and then resets.
func (d *Desktop) SetWhenDone(action string) error {
	switch power.Action(action) {
	case "", power.Sleep, power.Shutdown:
	default:
		return errors.New("unknown action " + action)
	}
	if err := power.Prepare(power.Action(action)); err != nil {
		return err
	}
	d.whenDone.Store(action)
	return nil
}

// WhenDone returns the pending action.
func (d *Desktop) WhenDone() string {
	action, _ := d.whenDone.Load().(string)
	return action
}

// PerformWhenDone runs action after the frontend's countdown, unless it was
// cancelled meanwhile.
func (d *Desktop) PerformWhenDone(action string) error {
	// Downloads may have been added during the countdown.
	items, err := d.List()
	if err != nil {
		return err
	}
	if !drained(items) {
		return errors.New("downloads were added during the countdown; the action stays pending")
	}
	if action == "" || !d.whenDone.CompareAndSwap(action, "") {
		return errors.New("the action was cancelled")
	}
	d.log.Info("queue done", "action", action)
	return power.Do(power.Action(action))
}

// afterStop runs when a download stops: it opens completed files if asked to
// and, once nothing is queued or running, announces the pending action.
func (d *Desktop) afterStop(item download.Item) {
	var err error
	// Pausing ("Pause all") is not finishing.
	if item.Status != download.StatusCompleted && item.Status != download.StatusFailed {
		return
	}
	if item.Status == download.StatusCompleted && d.mgr.Settings().Desktop.OpenWhenDone {
		// The server picks the name; only passive files open on their own.
		if passive(item.Destination) {
			err = d.app.Browser.OpenFile(item.Destination)
		} else {
			err = d.app.Env.OpenFileManager(item.Destination, true)
		}
		if err != nil {
			d.log.Warn("open completed download", "err", err)
		}
	}
	action := d.WhenDone()
	if action == "" {
		return
	}
	items, err := d.mgr.List(context.Background())
	if err != nil || !drained(items) {
		return
	}
	d.show()
	d.app.Event.Emit(eventQueueDone, action)
}

// passiveTypes open with a viewer, never as a program.
var passiveTypes = map[string]bool{
	".mp4": true, ".mkv": true, ".webm": true, ".mov": true, ".avi": true, ".m4v": true, ".ts": true,
	".mp3": true, ".flac": true, ".wav": true, ".m4a": true, ".aac": true, ".ogg": true, ".opus": true,
	".jpg": true, ".jpeg": true, ".png": true, ".gif": true, ".webp": true, ".heic": true,
	".pdf": true, ".txt": true, ".epub": true,
}

func passive(path string) bool { return passiveTypes[strings.ToLower(filepath.Ext(path))] }

// drained reports that no download is waiting or running.
func drained(items []download.Item) bool {
	for _, item := range items {
		if item.Status == download.StatusQueued || item.Status == download.StatusRunning {
			return false
		}
	}
	return true
}
