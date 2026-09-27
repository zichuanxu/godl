# godl — Design Contract

**`github.com/zichuanxu/godl`** · Apache-2.0 · Go 1.25 · open source, free

A fast, cross-platform download manager. The core engine, service, CLI, and desktop GUI
target feature parity with Internet Download Manager. The browser extension is deferred.

Revision 2 (2026-09-27) records the scope review in section 14. Where it conflicts with
revision 1, this document wins.

---

## 0. Project conventions

**English only, everywhere.** Source code, identifiers, comments, doc comments, commit
messages, error strings, log messages, CLI help, GUI copy, extension strings, issue
templates, and all documentation are written in English. No exceptions, including
scratch and internal notes.

**Zero spend.** godl is a personal open-source project. No paid certificates, developer
accounts, update servers, or hosted services. Free CI for public repositories is fine.

---

## 1. Shape

**One process, many surfaces.** The service (queue, SQLite store, `.part` files) is a
library that runs in one of two hosts:

- **GUI host** — the Wails app embeds the service in-process, owns the tray icon, and
  still listens on the loopback API so the CLI (and later the extension) can reach it.
- **Headless host** — `godl service` runs the same service without a GUI, for Linux,
  servers, and CI.

Clients:

- Wails/React GUI — calls the service through Wails bindings and receives events on the
  Wails event bus, fed by the same manager event stream. It does not use loopback HTTP.
- `godl` CLI — loopback HTTP + SSE.
- MV3 browser extension — **deferred** (section 6).

Autostart-at-login is a settings toggle, not an OS-registered service (no launchd job,
no Windows service).

**Platform tiers**

| Tier | Targets | Scope |
|---|---|---|
| 1 | `darwin/arm64`, `darwin/amd64`, `windows/amd64` | GUI, tray, installer, CLI, service |
| 1 | `linux/amd64`, `linux/arm64` | Headless service + CLI |
| — | Linux GUI | Not shipped; builds from source on a best-effort basis |

---

## 2. Layout

```text
internal/engine/   engine.go scheduler.go ranged.go single.go checkpoint.go checksum.go
                   retry.go transport.go prealloc_*.go diskfree_*.go runner.go
internal/hls/      hls.go playlist.go fetch.go
internal/remux/    optional system-ffmpeg remux
internal/manager/  manager.go filename.go destination.go
internal/settings/ settings.go (settings document, schedule rules)
internal/netproxy/ manual and system proxy
internal/store/    sqlite.go crypto.go migrations/
internal/api/      http.go sse.go auth.go
internal/service/  composition and lifecycle, shared by both hosts
cmd/godl/          headless service + CLI (cobra) — must NOT import wails
app/               wails v3 main + frontend/ (react-ts)
```

The public `downloader/` package is removed after the M1 cutover. godl makes no
library-API stability promise; everything lives under `internal/`.

---

## 3. Engine

### 3.1 Segmentation

**Full dynamic segmentation with work stealing.** No pre-chunking. N live connections
each own an interval `[cursor, end)`. When a connection finishes or fails, the largest
remaining in-flight interval is split at the midpoint of its unread bytes and the upper
half is reassigned. A connection that owns a range keeps reading up to its current `end`,
which the scheduler may lower concurrently.

The checkpoint is the **persisted list of remaining intervals** (per-connection cursors
included), not a completion bitmap. Progress and resume granularity are therefore bytes,
not chunks. The minimum split size is 1 MiB.

This replaces the chunk-queue + completion-bitmap model in `downloader/`. The cutover is
gated on the test suite in section 8.

### 3.2 Transport

**Force HTTP/1.1 on the segment transport** so each segment gets its own TCP
connection and its own congestion window — the entire reason multi-connection
downloading is fast. Under HTTP/2, Go multiplexes all segments onto one TCP
connection and the parallelism gain largely evaporates.

```go
segTransport := &http.Transport{
    TLSClientConfig:     &tls.Config{NextProtos: []string{"http/1.1"}},
    ForceAttemptHTTP2:   false,
    MaxConnsPerHost:     workers,
    MaxIdleConnsPerHost: workers,
}
```

The probe request keeps default negotiation so server capability is observed
honestly. If HTTP/1.1 is refused, fall back to single-stream HTTP/2. Probe and
segment responses must agree on the validator (section 3.3).

Both transports honour the configured proxy (section 4.3).

### 3.3 Validators: when to parallelize and when to resume

Follows RFC 9110 `If-Range` rules:

| Server offers | Parallel within a session | Resume across sessions |
|---|---|---|
| Range + known size + strong ETag | yes | yes, `If-Range: <etag>` |
| Range + known size + `Last-Modified` only | yes | yes, `If-Range: <date>` |
| Range + known size, no validator | yes | no — restart from zero |
| No Range, or unknown size | single stream | no — restart from zero |

Every segment response is checked for `206`, a matching `Content-Range` start, and the
same total size. A mismatch aborts the download as "resource changed". A user-supplied
checksum (section 3.8) is the final backstop.

### 3.4 Timeouts

**Stall timeout, not a request timeout.** A connection fails when it receives zero bytes
for 30 s (configurable). It then retries the *remaining* part of its interval, never the
whole interval, and single-stream downloads resume from the current offset when the
validator table allows it.

The stall clock only runs while waiting on the network, so time blocked on a rate
limiter (section 3.9) never counts.

**Slow-connection replacement.** When a connection's throughput stays below 30% of the
download's per-connection average for at least 3 s and its remaining interval is large
enough to split, its request is aborted and the range reconnects on a fresh connection;
any idle connection also steals the upper half. A connection that exhausts its retries
while others are alive (for example against a per-client connection cap) retires and
leaves its range to them; only the last connection standing fails the download.

### 3.5 Disk

Free-space precheck (`size + 64MiB`) before starting, then physical preallocation:

| Platform | Call |
|---|---|
| darwin | `fcntl(F_PREALLOCATE, F_ALLOCATEALL)` |
| windows | `SetFilePointerEx` + `SetEndOfFile`, sparse flag left off |
| linux | `fallocate(0, 0, size)` |
| fallback | `Truncate(size)` |

Deliberately **not** using Windows `SetFileValidData`: it requires a privilege and can
expose stale disk contents. `fsync` on checkpoint only, never per write. A final
checkpoint is written on cancel.

### 3.6 Salvaged from the existing implementation

Carried over because it took real effort to get right:

- `Range: bytes=0-0` capability probe (does not trust `Accept-Ranges` alone)
- Per-request `If-Range`
- `206` / `Content-Range` / total-size validation
- Retry classifier with `Retry-After` and exponential backoff with full jitter
- Sync-then-atomic-rename commit
- Single-stream fallback
- Defenses against mid-download resource change, ignored Range, short reads,
  bad `Content-Range`, and non-identity `Content-Encoding`

**Destination locking** moves from "lock file exists" to an OS advisory lock
(`flock` on Unix, `LockFileEx` on Windows), which the kernel releases when the process
dies. No stale locks after `SIGKILL` or a crash.

### 3.7 Sources

```go
type Source interface {
    Probe(ctx context.Context) (Plan, error)
    Parts() []Part                  // byte ranges OR segment list
    Fetch(ctx context.Context, p Part, w io.WriterAt) error
}
```

Implementations: `httpRange`, `hlsSegments`. **DASH is deferred**: its audio and video
tracks are separate and need a muxer.

HLS resume granularity is **whole segments**. AES-128 key fetch and decryption are
included. MPEG-TS segments are concatenated byte-for-byte into a playable `.ts`; fMP4
segments are concatenated after their init segment. Segments stream to disk (never
whole in memory) and finished ones are kept in a `dest.hls/` directory, fingerprinted by
the playlist without query strings, so a playlist re-fetched with fresh CDN tokens still
resumes; a structurally changed playlist restarts the download. Request headers go only
to the playlist's host; segment and key hosts named by the playlist get portable headers
only. The engine hands a URL ending in `.m3u8`, or a
probe answered with an HLS `Content-Type`, to the HLS source. Separate audio and
subtitle renditions (`EXT-X-MEDIA`) are not downloaded.

### 3.8 Integrity

Optional checksum in `algo:hex` form, accepted by the CLI and the service API:
`sha256`, `sha512`, `sha1`, `md5`. Verified after the download, before the atomic rename.

### 3.9 Rate limiting

Hierarchical token buckets applied by wrapping the network reader, so they compose:

```text
resp.Body -> limitedReader{global, perDownload} -> WriteAt
```

`golang.org/x/time/rate` provides bucket, burst, and live `SetLimit` for scheduled
off-peak caps. Reads are waited in slices no larger than the bucket burst. No custom
limiter.

### 3.10 Remux

**No bundled ffmpeg.** If an `ffmpeg` executable is found on `PATH` (or configured in
settings), the GUI offers "convert to MP4" for finished HLS downloads:

```bash
ffmpeg -i in.ts -c copy -movflags +faststart out.mp4
```

It runs as a child process, so failures stay isolated. Without ffmpeg the `.ts` file is
the final output. No LGPL obligations arise because nothing is redistributed.

---

## 4. Manager

Single ordered queue with a max-concurrent setting, three priority levels, and a
per-host connection ceiling (so four downloads at eight connections each do not
become thirty-two sockets against one server).

| Setting | Default | Range |
|---|---|---|
| Connections per download | 8 | 1–32 |
| Connections per host (all downloads) | 16 | configurable, also per site |
| Minimum split size | 1 MiB | — |

```text
scheduler tick:
  while active < maxConcurrent:
    next = highest priority, then FIFO
    if hostConns[next.host] >= hostCap: skip
    start(next)
```

The scheduler runs on an injectable clock so tests are deterministic.

### 4.1 Lifecycle

```text
queued -> running -> completed
            |  ^
            v  |
          paused        running -> failed -> (retry) -> queued
any non-running state -> deleted (optionally removing the file)
```

- Transitions are validated; for example a `completed` item cannot be paused. A
  download that finishes while a pause races it is recorded as `completed`.
- At most one item may use a destination; a second `add` is rejected.
- Items left `running` without a runner (after a crash, or a failed store write)
  are reset to `queued` at start and on every flush tick, and resume.
- A destination locked by another process requeues the item with a short
  backoff instead of failing it.
- Destination confinement (5.1) is re-checked when a download starts and before
  its files are deleted.
- Shutdown cancels runners, **waits for them to exit and checkpoint**, then closes the
  store. If the listener fails, the process exits non-zero.
- Progress lives in memory and is pushed as events. SQLite is written on state
  transitions and asynchronously every 5 s, never under an engine lock. SQLite runs in
  WAL mode with `synchronous=NORMAL` and a `busy_timeout`.
- Store errors are logged and surfaced; a failing store never produces a hot retry loop.

### 4.2 Scheduling

IDM-style scheduler: start or stop the queue at set times, and apply time-of-day speed
limits through `SetLimit`.

### 4.3 Network settings

- Proxy: manual (HTTP / HTTPS / SOCKS5) or **system proxy**, read from macOS
  `SystemConfiguration` and the Windows Internet settings. Go's
  `ProxyFromEnvironment` alone ignores both. Environment variables still apply on Linux.
- PAC scripts are deferred (they need an embedded JavaScript engine).
- Per-site settings: connection count, extra headers, credentials, host cap.

### 4.4 Filenames

Resolution order: `Content-Disposition` (including RFC 5987 `filename*`) → URL path →
host-derived fallback.

Then **hard sanitization** — the filename is attacker-controlled input:

- Strip path separators and `..`
- Reject Windows reserved names (`CON`, `PRN`, `AUX`, `NUL`, `COM1`-`COM9`, `LPT1`-`LPT9`)
- Trim trailing dots and spaces
- Cap length in bytes, not runes
- Normalize Unicode on macOS

### 4.5 Placement

Category routing by extension (Video / Music / Documents / Compressed / Programs)
under a configurable root, overridable per download, with an "always ask" toggle.
Collisions resolve to `name (1).ext`: a name is free when no file, `.part` file, or
queued download uses it. The manager lock serializes adds, so two downloads cannot
both win, and the engine's link-based commit (3.5) fails instead of overwriting a
file another process created meanwhile.

---

## 5. Service API

Loopback HTTP + JSON for commands. **Server-Sent Events for progress** — stdlib only.

```text
GET    /v1/ping
GET    /v1/downloads                 list
POST   /v1/downloads                 add
POST   /v1/downloads/{id}:pause
POST   /v1/downloads/{id}:resume
POST   /v1/downloads/{id}:retry
DELETE /v1/downloads/{id}?files=true
PATCH  /v1/downloads/{id}            priority, speed limit
GET    /v1/events                    text/event-stream
```

**Events: snapshot, then deltas.** On connect the stream sends one `snapshot` event with
every download's current state, then `progress` and lifecycle deltas. Events are state,
not commands, so no `Last-Event-ID` replay buffer is kept. A subscriber that falls
behind is disconnected and reconnects to a fresh snapshot, instead of silently losing
events.

```text
id: 1042
event: progress
data: {"id":"x","done":123,"total":456,"bps":4500000}
```

### 5.1 Local authorization

Loopback alone is not a boundary: any local process can connect, and a web page can
send a CORS-simple `text/plain` POST or use DNS rebinding. Therefore, on every request:

- `Authorization: Bearer <token>`, where the token is generated on first start and
  stored in the app data directory with mode `0600`; the CLI reads it from there. The
  SSE token may ride a query parameter because `EventSource` cannot send headers, and is
  excluded from logging.
- `Host` must be `127.0.0.1:<port>` or `localhost:<port>` (DNS-rebinding defense).
- Commands require `Content-Type: application/json`.
- Requests carrying an `Origin` header are rejected until extension pairing exists.
- `destination` must resolve inside the configured download root (or a directory the
  user picked in the GUI), and never inside known autostart locations.

### 5.2 Extension pairing — deferred

The revision 1 pairing flow (6-digit code, Bearer token, `chrome-extension://` Origin
allowlist) lands with the extension.

---

## 6. Browser extension — deferred

Without an extension, authenticated downloads are added by:

- **cURL import** — paste a request copied with the browser's DevTools
  "Copy as cURL"; the URL, cookies, referer, user agent, and headers are captured.
- **Clipboard monitor** — detects copied URLs and cURL commands and offers to add them.

Deferred with the extension: automatic capture, the video overlay, and "download all
links on page".

---

## 7. Secrets and logging

Request headers and cookies needed to resume a download are stored in SQLite under
AES-GCM, with the key held in the macOS Keychain or protected by Windows DPAPI via
`go-keyring`. On headless Linux without a keyring, secrets are kept in memory only and a
resume after restart asks for them again. The database file alone is useless if copied.

When a resume gets `401`/`403`, the UI shows "re-authenticate (paste cURL again)" rather
than a bare error.

Logging is `slog` JSON to a size-capped rotating file (10MB × 3) in the app data
directory, with a level selector in settings and a log pane in the GUI offering a
re-scrubbing "copy for bug report" button.

**Never logged:** `Authorization`, `Cookie`, `Set-Cookie`, the API token, and signed
query parameters (`X-Amz-Signature`, `token`, `sig`). Users paste logs into public issues.

**Logged:** download identity, resolved host, negotiated protocol, size, validator,
segment timings, retry reasons, status codes, throughput, checkpoint duration,
verification result.

---

## 8. Testing

Fault injection against `httptest` servers that misbehave on purpose:

- Truncated body
- Wrong `Content-Range`
- Range silently ignored
- gzip applied to a byte range
- Validator changing mid-download
- 503 with `Retry-After`
- Slow-loris and stalled connections
- Hostile HLS: key fetch failure, missing segment, playlist change

Plus a **randomized crash-resume test** that aborts at arbitrary offsets, resumes, and
asserts the final SHA-256 always matches a golden digest (×100). This is the only way
to trust an interval-based checkpoint.

**Performance gate** — `tools/bench`, a reproducible benchmark run in CI (a local
server capping each connection's bandwidth, plus an uncapped run for CPU cost),
compared with `aria2c -x16 -s16`:

- throughput ≥ 95% of aria2c on the same URL and network
- less than one CPU core at 1 Gbps
- RSS below 100 MB

| Layer | Approach |
|---|---|
| engine | httptest + fault injection + randomized crash-resume + benchmark |
| manager | fake clock, deterministic scheduler |
| store | temp SQLite, migrations up and down |
| api | contract tests including authorization denial, Host and Origin rejection |
| CI | `-race` on `macos-latest`, `windows-latest`, `ubuntu-latest` |
| GUI | manual checklist on macOS and Windows |

---

## 9. Dependencies

| Module | Used by | Purpose |
|---|---|---|
| `modernc.org/sqlite` | service | pure-Go store, no cgo, clean cross-compile |
| `golang.org/x/time/rate` | engine | token buckets |
| `golang.org/x/sys` | service | platform syscalls, advisory locks, system proxy |
| `github.com/zalando/go-keyring` | service | Keychain / DPAPI |
| `github.com/spf13/cobra` | CLI | subcommands, help, completions |
| `github.com/wailsapp/wails/v3` | **`app/` only** | GUI shell with system tray, **pinned to an exact beta version** |

Wails v3 is chosen over v2 because v2 has no system tray API. v3 is still beta as of
2026-09; the version is pinned and upgraded deliberately.

**CI gate:** `go list -deps ./cmd/godl` must not contain `wails`. The headless binary
stays lean and cross-compiles trivially.

---

## 10. Distribution

Open source and free. Apache-2.0 for its express patent grant and its `NOTICE`
mechanism.

```text
LICENSE         Apache-2.0
NOTICE          godl attribution
```

**Unsigned builds.** No Apple Developer account and no Windows code-signing certificate.

| Channel | Artifact | Notes |
|---|---|---|
| GitHub Releases | CLI archives for all tier-1 targets (goreleaser), dmg, zip, Windows installer | from v0.1 for the CLI |
| `zichuanxu/homebrew-tap` | cask (GUI) and formula (CLI) | Homebrew's official cask repository removes casks that fail Gatekeeper checks from 2026-09-01, so the GUI cannot go there unsigned |
| `zichuanxu/scoop-bucket` | Windows GUI and CLI | |

Package names: Homebrew formula `godl` (CLI) and cask `godl-desktop` in
`zichuanxu/homebrew-tap`; Scoop `godl` and `godl-desktop` in `zichuanxu/scoop-bucket`.
winget is not offered. `tools/packaging` renders all of
them from the release checksums; the release workflow pushes the tap and bucket when a
`TAP_TOKEN` secret exists and attaches the manifests to the release either way. Releases start as drafts and are published only after
the install checks pass, so the update banner never points at a release without
installers. The release workflow also installs the dmg and the installer on clean GitHub runners,
launches the app, and downloads a file through it (the M6 install-to-download check).

docs/install.md documents the Gatekeeper workaround (allow in System Settings, or
`xattr -dr com.apple.quarantine /Applications/godl.app`) and the SmartScreen
"More info → Run anyway" path.

**Updates:** weekly poll of the GitHub Releases API, semver compare, dismissible
banner linking to release notes. No update server, no signing keys, no
self-replacement code — an updater that bricks an install is the scariest class of
desktop bug.

`godl version` prints the version, commit, and build date injected at link time.

---

## 11. Milestones

| | Tag | Deliverable | Done when |
|---|---|---|---|
| **M0** | — ✅ | Current engine wired through service + SQLite + API + minimal React table. | Done. |
| **M0.5** | v0.1 ✅ | Hardening: git, green `go vet`/`go test`, local authorization (5.1), advisory locks, lifecycle and restart recovery (4.1), async progress persistence, `slog` with redaction, LICENSE/NOTICE, `godl version`, README fixes, CI matrix, goreleaser CLI release. | CI green with `-race` on three OSes; API contract tests cover authorization denial; `kill -9` mid-download, restart, and the download resumes. |
| **M1** | v0.2 ✅ | Engine rewrite: work-stealing intervals, stall timeout and slow-connection replacement, validator table, HTTP/1.1 forcing with h2 fallback, free-space precheck, preallocation, checksums; `downloader/` removed. | Fault-injection suite and crash-resume ×100 green on three OSes; performance gate met. |
| **M2** | v0.3 ✅ | Manager and network: priorities, connection defaults and host cap, hierarchical limiters, time-based scheduler, filename resolution and placement, manual and system proxy, per-site settings. | Deterministic fake-clock scheduler tests; system-proxy integration tests on macOS and Windows. |
| **M3** | v0.6 ✅¹ | GUI: Wails v3, in-process service, bindings and event bus, tray/menubar, notifications, sparkline, settings, drag-drop, open/reveal folder, cURL import, clipboard monitor, keyring-encrypted secrets. | Manual checklist passes on macOS and Windows. |
| **M4** | v0.6 ✅¹ | Extras: batch URL patterns, queue import/export, completion actions (open, sleep, shut down). | Unit tests plus manual checklist. |
| **M5** | v0.6 ✅ | HLS: playlist parsing, AES-128, TS/fMP4 concatenation, optional system-ffmpeg remux. | Hostile-HLS fault tests green for clear and encrypted streams. |
| **M6** | v1.0 ✅ | Release: dmg, zip, Windows installer, Homebrew tap, scoop, version-check banner, install docs. | Install-to-download flow verified on clean macOS and Windows VMs. |

M6's install-to-download check runs in the release workflow on fresh GitHub macOS and
Windows runners: it installs the dmg and the NSIS installer, launches the app, downloads
a file through it, verifies it, and (on Windows) uninstalls.

¹ M3, M4, and M5 shipped together in v0.6. Their automated checks pass in CI (unit tests,
the desktop build on macOS and Windows, current bindings); the manual checklist
(`docs/desktop-checklist.md`) still has to be run on macOS and Windows.

Deferred beyond v1.0: browser extension and pairing, DASH, bundled ffmpeg, PAC,
global hotkey, download-all-links, code signing, Linux GUI.

---

## 12. Open items

1. **Wails v3 stability** — track the 3.0 release; re-pin when it lands.
2. **Store accounts** (Chrome Web Store, Firefox AMO, Edge Add-ons) — only when the
   extension is picked up. The Chrome Web Store fee conflicts with the zero-spend rule;
   decide then.

Closed by revision 2: repository ownership (moved from `amemiya02` to `zichuanxu`;
module path `github.com/zichuanxu/godl`), notarization and signing certificates (not pursued), minimal
ffmpeg build script (ffmpeg is not bundled), "v1 spans all four feature bundles" (the
extension and video extras are deferred).

---

## 13. Accepted tradeoffs

1. **Full dynamic segmentation** discards the tested chunk-bitmap crash recovery. The
   revision 2 review re-confirmed it: the fixed 8 MiB chunk is the root cause of the
   request-timeout, progress-granularity, and resume-granularity defects, so patching
   the old engine would rebuild the new one piecemeal. The randomized crash-resume suite
   exists to rebuild the lost confidence.
2. **Relaxed validators** (3.3) parallelize downloads without a strong ETag. Within one
   session the risk is a resource changing mid-transfer, which the total-size and
   `Content-Range` checks catch in practice; cross-session resume still needs a validator.
3. **Unsigned releases** cost first-run friction on macOS and Windows and rule out
   Homebrew's official cask repository, in exchange for zero spend.
4. **Wails v3 beta** is required for the tray, at the cost of tracking a pre-1.0 API.
5. **No SSE replay**: a reconnecting client gets a snapshot, not missed history.
   Lifecycle history, if ever needed, belongs in the store.
6. **HLS without bundled ffmpeg**: output is `.ts` unless the user has ffmpeg.

---

## 14. Revision 2 scope review (2026-09-27)

A read-only audit found that the only live download path is `downloader/downloader.go`.
The M1/M2 files under `internal/engine` and `internal/manager` were written but not
wired, and partly incomplete (no re-splitting of owned intervals, a limiter that spins on
requests larger than its burst, a check-then-create collision loop). `go vet ./...`
failed on `app/` tests that reference missing symbols.

Decisions taken in the review:

| Topic | Decision |
|---|---|
| Scope | Core + GUI at IDM parity; extension deferred |
| Users | Desktop (macOS, Windows) and headless Linux; no library API promise |
| Speed | Performance gate against aria2c (section 8) |
| Protocols | HTTP(S) and HLS; DASH deferred |
| GUI | Wails v3 beta, pinned; service embedded in the GUI process; bindings, not HTTP |
| Linux | CLI and service tier 1; no GUI |
| Auth downloads | cURL import + clipboard monitor; secrets encrypted with the OS keyring |
| Local API | Token, Host allowlist, JSON-only, Origin rejection, destination confinement |
| Engine | Finish the interval engine; stall timeout; RFC 9110 validator table |
| Connections | 8 per download (max 32), 16 per host, 1 MiB minimum split |
| Distribution | Unsigned; own Homebrew tap and Scoop bucket |
| Order | Hardening (M0.5) before the engine rewrite; proxy in M2; extras before HLS |
