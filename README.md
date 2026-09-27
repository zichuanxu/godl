# godl

A fast download manager for macOS, Windows, and Linux, written in Go: a desktop app (macOS, Windows) and a CLI with a background service.

- Up to 32 connections per download, rebalanced as they finish; byte-level resume, even after a crash.
- Queue with priorities, per-host connection caps, speed limits, and a schedule.
- HLS (`.m3u8`, AES-128) to one `.ts` file, optional MP4 conversion with ffmpeg.
- Desktop app: tray icon, notifications, drag and drop, cURL import, clipboard monitor, batch URLs, sleep or shut down when done.

## Install

| | |
| --- | --- |
| macOS app | `brew install --cask zichuanxu/tap/godl-desktop`, or the `.dmg` from [Releases](https://github.com/zichuanxu/godl/releases/latest) |
| Windows app | `scoop bucket add zichuanxu https://github.com/zichuanxu/scoop-bucket` then `scoop install zichuanxu/godl-desktop`, or the installer from Releases |
| CLI | `brew install zichuanxu/tap/godl`, `scoop install zichuanxu/godl`, or an archive from Releases |

The builds are unsigned; [docs/install.md](docs/install.md) has the one-time Gatekeeper and SmartScreen steps.

## Quick start

```bash
godl download https://example.com/file.iso ./file.iso   # one-off download
godl service &                                          # or run the queue in the background
godl add https://example.com/file.iso
godl list
```

The desktop app runs the same service, so `godl add` and `godl list` also work while it is open. [docs/usage.md](docs/usage.md) covers every command, the settings, and the HTTP API.

## Build from source

Requires Go 1.25+. The desktop app also needs Node.js 22 and, on macOS, the Xcode command line tools.

```bash
go test ./...
go build -o godl ./cmd/godl
```

Run the desktop app from source (it lives in `app/`, its own Go module):

```bash
cd app/frontend && npm ci && npm run build && cd ..
go run .
```

For live reload of the UI, keep `npm run dev` running in `app/frontend` and start the app with `FRONTEND_DEVSERVER_URL=http://127.0.0.1:9245 go run .`. Quit any installed godl or `godl service` first: they share port 51000 and a single-instance lock.

After changing a bound Go method or type, regenerate the TypeScript bindings (CI checks them):

```bash
go install github.com/wailsapp/wails/v3/cmd/wails3@v3.0.0-beta.26
cd app && wails3 generate bindings -clean=true -ts -f '-tags production'
```

Release packages come from `app/scripts/package-macos.sh` and `app/scripts/package-windows.sh`. The design and roadmap are in [DESIGN.md](DESIGN.md); [docs/desktop-checklist.md](docs/desktop-checklist.md) is the manual test list.

## Repository layout

```text
cmd/godl/             Cobra CLI: service, add, list, set, pause, resume, retry, delete, settings, export, import, download, version
internal/download/    Shared queue model, errors, and event contract
internal/engine/      Download engine: work-stealing ranges, checkpoints, validation
internal/filelock/    OS advisory file locks
internal/logging/     slog setup and URL redaction
internal/store/       SQLite persistence
internal/manager/     Queue, priorities, host caps, limits, schedule, naming, confinement
internal/settings/    Settings document and schedule rules
internal/netproxy/    Manual and system proxy selection
internal/hls/         HLS playlists, AES-128, TS/fMP4 concatenation, segment resume
internal/remux/       Optional ffmpeg remux of .ts to .mp4
internal/batch/       Batch URL patterns
internal/queuefile/   Queue import and export
internal/power/       Sleep and shut down
internal/update/      Weekly release check for the desktop banner
internal/quarantine/  Marks completed files as downloaded from the internet
internal/secrets/     AES-GCM sealing with a key in the OS keyring
internal/curlimport/  "Copy as cURL" parser
internal/api/         Authenticated loopback JSON and SSE API
internal/client/      Service client
internal/service/     Service composition, lifecycle, paths, and token
tools/bench/          Performance gate against aria2c
tools/packaging/      Homebrew and Scoop manifests for a release
docs/                 Install guide, usage reference, desktop checklist
app/                  Desktop app (Wails v3 module): bindings, tray, React frontend
DESIGN.md             Design contract and M0–M6 roadmap
```

## License

Apache-2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE).
