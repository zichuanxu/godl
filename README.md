# godl

`godl` is a fast, cross-platform download manager written in Go. It has two surfaces today:

- a direct command-line downloader for one-off transfers;
- a loopback service with a SQLite-backed queue, an authenticated JSON API, SSE progress events, and CLI client commands.

The engine splits a download across up to 32 HTTP/1.1 connections and rebalances them continuously: when a connection finishes, it takes over half of the largest remaining range, and connections far slower than the rest are replaced. Progress is checkpointed at byte granularity, so an interrupted download resumes where it stopped, even after a crash. Responses are validated against the probed ETag or Last-Modified, `Content-Range`, and size, and an optional checksum is verified before the file is committed atomically.

The service queue orders downloads by priority, shares a per-host connection cap across downloads, applies global and per-download speed limits (including time-of-day rules and a queue window), names files from the server, and connects through a manual or the system proxy.

HLS streams (`.m3u8`, including AES-128 encryption) download as one playable `.ts` file with per-segment resume; with ffmpeg installed, the desktop app converts them to MP4 without re-encoding.

The desktop app (macOS and Windows) runs the same service in-process, with a tray icon, notifications, drag and drop, cURL import, a clipboard monitor, batch URL patterns, queue import and export, and sleep or shut down when the queue is done.

The roadmap and design contract live in [DESIGN.md](DESIGN.md).

## Install

| | |
| --- | --- |
| macOS app | `brew install --cask zichuanxu/tap/godl-desktop`, or the `.dmg` from [Releases](https://github.com/zichuanxu/godl/releases/latest) |
| Windows app | `winget install zichuanxu.godl`, `scoop install zichuanxu/godl-desktop` (after `scoop bucket add zichuanxu https://github.com/zichuanxu/scoop-bucket`), or the installer from Releases |
| CLI (macOS, Linux, Windows) | `brew install zichuanxu/tap/godl`, `scoop install zichuanxu/godl`, or an archive from Releases |

The builds are unsigned, so the first launch needs one confirmation: [docs/install.md](docs/install.md) has the steps for Gatekeeper and SmartScreen.

## Requirements

- Go 1.25 or newer.
- For the desktop app: Node.js 22 and npm; on macOS the Xcode command line tools (cgo).

## Build and test

```bash
go vet ./...
go test -race ./...
go build -o godl ./cmd/godl
./godl version
```

`go test -short ./...` skips the slower end-to-end tests.

The performance gate compares godl with `aria2c -x16 -s16` on a local server that caps each connection's bandwidth (CI runs it on every push):

```bash
go build -o godl ./cmd/godl
go run ./tools/bench -godl ./godl -gate
```

## Desktop app

The desktop app lives in `app/` as its own Go module, so the CLI never depends on Wails (v3, pinned to `v3.0.0-beta.26`).

```bash
cd app/frontend && npm ci && npm run build && cd ..
go build -tags production -o bin/godl .                                     # macOS (cgo)
GOOS=windows CGO_ENABLED=0 go build -tags production -ldflags "-H windowsgui" -o bin/godl.exe .
```

Release packages come from `app/scripts/package-macos.sh` (universal `.app`, `.dmg`, `.zip`) and `app/scripts/package-windows.sh` (`.exe` with icon and manifest, portable `.zip`, NSIS installer); the release workflow runs them, then installs each package on a clean runner and downloads a file through it (`app/scripts/smoke-*.sh`).

After changing a bound Go type or method, regenerate the TypeScript bindings (CI checks they are current):

```bash
go install github.com/wailsapp/wails/v3/cmd/wails3@v3.0.0-beta.26
cd app && wails3 generate bindings -clean=true -ts -f '-tags production'
```

Beyond the CLI's features, the app offers "When done: Sleep / Shut down" (after a 30-second cancellable countdown, once, and only when downloads finish rather than pause; on macOS, shutting down needs the Automation permission requested when you pick it), opening media and documents when they finish (other files are revealed instead), and "Convert to MP4" for finished `.ts` streams using ffmpeg from `PATH` or Settings (ffmpeg is never bundled).

The app embeds the download service and still serves the loopback API, so the CLI commands below work while it runs; a separate `godl service` must not be running at the same time. Closing the window keeps downloads going in the tray; Quit stops them after they checkpoint. Logs go to `godl.log` in the data directory. [docs/desktop-checklist.md](docs/desktop-checklist.md) is the manual test list.

## Direct downloader

The `download` command runs the engine without the service:

```bash
./godl download \
  --connections 8 \
  --checksum 'sha256:<64-character-hex-digest>' \
  'https://example.com/large-file.bin' \
  './large-file.bin'
```

| Flag | Default | Meaning |
| --- | --- | --- |
| `--connections` | `8` | Parallel connections, at most 32. |
| `--min-split-mib` | `1` | Smallest range handed to one connection. |
| `--stall-timeout` | `30s` | Reconnect a connection that receives nothing for this long. |
| `--attempts` | `5` | Consecutive failed requests without progress before giving up. |
| `--checksum` | none | `algo:hex` with `sha256`, `sha512`, `sha1`, or `md5`. |
| `--resume-key` | the URL | Stable identity for expiring signed URLs. |
| `--header` | none | Extra request header; repeatable. |
| `--speed-limit` | `0` | Bytes per second, with `K`, `M`, or `G`; `0` is unlimited. |
| `--proxy` | `system` | `system`, `none`, or a `http://`, `https://`, or `socks5://` proxy URL. |
| `--overwrite` | off | Replace an existing destination. |

```bash
./godl download --header 'Authorization: Bearer TOKEN' URL OUTPUT
./godl download --resume-key 'bucket/object/version-42' SIGNED_URL OUTPUT
```

An HLS playlist, recognised by a `.m3u8` URL or an HLS `Content-Type`, is downloaded segment by segment into one MPEG-TS file (fMP4 streams get their init segment first). A master playlist picks its highest-bandwidth variant; live streams and SAMPLE-AES are refused. Separate audio and subtitle renditions are not downloaded, and an fMP4 stream keeps the `.ts` name chosen before its playlist is read (convert it to MP4 for players that trust extensions). Cookies and other request headers are sent only to the playlist's host.

Completed files are marked as downloaded from the internet (the `com.apple.quarantine` attribute on macOS, a `Zone.Identifier` stream on Windows), so Gatekeeper and SmartScreen check downloaded programs as they would a browser download.

While a transfer is active the engine keeps `OUTPUT.part`, `OUTPUT.part.meta`, and `OUTPUT.lock` (for HLS, finished segments in `OUTPUT.hls/`). The lock is an OS advisory lock, so a crashed or killed process never leaves a lock that blocks the next run. The engine checks free space and preallocates the file before downloading.

When parallel download and resume apply:

| Server offers | Parallel | Resume after a restart |
| --- | --- | --- |
| Ranges, known size, strong ETag | yes | yes |
| Ranges, known size, `Last-Modified` only | yes | yes |
| Ranges, known size, no validator | yes | no, restarts from zero |
| No ranges, or unknown size | single connection | no, restarts from zero |

## Background service and CLI client

```bash
./godl service \
  --listen 127.0.0.1:51000 \
  --download-root "$HOME/Downloads"
```

| Flag | Default | Meaning |
| --- | --- | --- |
| `--listen` | `127.0.0.1:51000` | Loopback address; non-loopback addresses are rejected. |
| `--database` | `<config dir>/godl/godl.db` | SQLite queue. |
| `--download-root` | `~/Downloads` | Directory downloads may be written under. Repeatable. |
| `--log-level` | `info` | JSON logs on stderr: `debug`, `info`, `warn`, `error`. |
| `--token-file` | `<config dir>/godl/token` | API token, created on first start with owner-only permissions. |

`<config dir>` is `~/Library/Application Support` on macOS, `%AppData%` on Windows, and `$XDG_CONFIG_HOME` or `~/.config` on Linux.

Client commands read the same token file and talk to `http://127.0.0.1:51000` unless `--address` or `--token-file` say otherwise:

```bash
./godl add 'https://example.com/file.bin'                 # named by the server
./godl add 'https://example.com/file.bin' ~/Downloads/f.bin --priority high --speed-limit 2M
./godl add URL --dir ~/Downloads/isos --connections 16 --header 'Cookie: session=...'
./godl list
./godl set <id> --priority low --speed-limit 0
./godl pause <id>
./godl resume <id>
./godl retry <id>
./godl delete <id> --files
./godl settings get > settings.json
./godl settings set settings.json
./godl add --batch 'https://example.com/photos/img[001-120].jpg' --dir ~/Downloads/photos
./godl export queue.json                                   # unfinished downloads, never their headers
./godl import queue.json                                   # or a text file with one URL per line
```

Batch patterns expand left to right: `[1-10]`, zero-padded `[001-120]`, stepped `[0-100:5]`, letters `[a-z]`, and alternatives `{cd,dvd}`, up to 10,000 URLs.

Without `OUTPUT`, the file name comes from `Content-Disposition` (including RFC 5987 `filename*`), then the URL path, then the host. It is sanitized (no path separators, `..`, control characters, or Windows reserved names; at most 200 bytes) and placed in `--dir`, or in a category folder under the first download root: `Video`, `Music`, `Documents`, `Compressed`, or `Programs` by extension, the root itself otherwise. A name that is taken on disk or by another queued download becomes `name (1).ext`.

Relative paths are resolved against the client's working directory. On restart, the service resumes downloads that were running when it stopped, including after a crash.

### Settings

`godl settings set` replaces the whole document, so edit the output of `settings get`:

```json
{
  "maxConcurrent": 3,
  "connections": 8,
  "hostConnections": 16,
  "speedLimit": 0,
  "schedule": {
    "start": "01:00",
    "stop": "07:00",
    "speedRules": [{ "from": "09:00", "to": "18:00", "limit": 1048576 }]
  },
  "proxy": { "mode": "system" },
  "sites": [
    { "host": "example.com", "connections": 4, "hostConnections": 4,
      "headers": { "Referer": "https://example.com/" }, "username": "me", "password": "secret" }
  ]
}
```

| Field | Meaning |
| --- | --- |
| `maxConcurrent` | Downloads running at once, 1–64. |
| `connections` | Default connections per download, 1–32. |
| `hostConnections` | Connections to one host across all downloads, 1–64. A download takes what its host has left; with none left it waits. |
| `speedLimit` | Global cap in bytes per second; `0` is unlimited. |
| `schedule.start`, `schedule.stop` | Local `HH:MM` window in which the queue runs, wrapping past midnight. Outside it running downloads return to the queue. Leave both empty to run all day. |
| `schedule.speedRules` | Time-of-day global limits; the first matching rule wins over `speedLimit`. |
| `proxy.mode` | `system` (macOS and Windows settings, else `HTTP_PROXY`/`HTTPS_PROXY`), `none`, or `manual` with `proxy.url`. Loopback addresses never use a proxy. |
| `sites` | Overrides for a host and its subdomains: connections, host cap, extra headers, and basic-auth credentials. |

Per-download priority, connections, and speed limit come from `add` or `set`; a new speed limit applies to a running download at once. Site passwords are shown as `********` by `settings get`; sending that value back keeps the stored password.

Download headers (such as cookies) and site credentials are encrypted in the database with AES-GCM under a key kept in the OS keyring (Keychain, Windows Credential Manager, or the Secret Service). Without a keyring, as on a headless server, the key lives in memory only: after a restart those values are gone and a download that needs them fails with 401 or 403 until its headers are supplied again.

## HTTP API

Every endpoint except `/v1/ping` requires `Authorization: Bearer <token>`. Commands other than `GET` require `Content-Type: application/json`.

| Method | Path | Behavior |
| --- | --- | --- |
| `GET` | `/v1/ping` | `{"status":"ok"}` |
| `GET` | `/v1/downloads` | Lists downloads with live progress. |
| `POST` | `/v1/downloads` | Body `{"url":"...","destination":"/absolute/path"}`, or `"directory"` or neither for a server-named file; optional `priority` (`-1`, `0`, `1`), `connections`, `speedLimit`, `checksum`, `headers`. Returns `202` with the queued item. |
| `POST` | `/v1/downloads/{id}:pause` | Pauses a queued or running download. `204` |
| `POST` | `/v1/downloads/{id}:resume` | Requeues a paused download. `204` |
| `POST` | `/v1/downloads/{id}:retry` | Requeues a failed download. `204` |
| `PATCH` | `/v1/downloads/{id}` | Body with any of `priority`, `connections`, `speedLimit`. `204` |
| `DELETE` | `/v1/downloads/{id}` | Deletes a stopped download; `?files=true` also removes partial data and, for completed downloads, the file. `204` |
| `GET` | `/v1/settings` | The settings document. |
| `PUT` | `/v1/settings` | Replaces the settings; returns them, or `422` with every validation error. |
| `GET` | `/v1/events` | `text/event-stream`; also accepts `?token=` because `EventSource` cannot send headers. |

Errors use `{"error":{"code":"...","message":"..."}}`: `404` for an unknown ID, `409` for a command the current status forbids (for example pausing a completed download, or deleting one that is still stopping), and `422` for an invalid URL, a destination outside the download roots, or a destination another download already uses.

The event stream starts with one `snapshot` event holding every download, followed by `updated`, `progress`, and `deleted` deltas:

```text
id: 12
event: progress
data: {"id":"...","status":"running","completed":123,"total":456,...}

```

A client that falls behind is disconnected and should reconnect for a fresh snapshot; missed events are not replayed.

## Security model

- The service listens on loopback only and requires the bearer token on every command.
- Requests whose `Host` is not `127.0.0.1`, `localhost`, or `::1` are rejected (DNS-rebinding defense), and so is any request carrying an `Origin` header, which blocks browser pages.
- Destinations must resolve, after following symlinks, inside a `--download-root`, and never inside known autostart directories.
- Logs never contain request headers, and URLs are logged with credentials and signature parameters redacted. Do not share the token file.

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
tools/packaging/      Homebrew, Scoop, and winget manifests for a release
docs/                 Install guide and desktop checklist
app/                  Desktop app (Wails v3 module): bindings, tray, React frontend
DESIGN.md             Design contract and M0–M6 roadmap
```

## License

Apache-2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE).
