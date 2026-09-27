# godl

`godl` is a Go download manager with two M0 surfaces:

- a direct command-line downloader for one-off transfers;
- a loopback service with a SQLite-backed queue, JSON commands, and SSE progress events.

The existing downloader supports capability probing, validated byte ranges, bounded concurrency, retries, checkpoints, resume, SHA-256 verification, destination locking, and safe finalization. The service owns queue state and invokes that engine through a small runner boundary.

The complete roadmap is recorded in [DESIGN.md](DESIGN.md). M1–M7 features are not yet shipped.

## Requirements

- Go 1.25 or newer (`go.mod` declares Go 1.25).
- Node.js and npm only when building the optional React frontend under `app/frontend`.

## Build and test

```bash
go test ./...
go test -race ./...
go vet ./...
go build -o godl ./cmd/godl
```

Build the frontend separately:

```bash
cd app/frontend
npm install
npm run build
```

The frontend build writes ignored static assets to `app/frontend/dist`.

## Direct downloader

The `download` command runs the existing downloader without starting the service:

```bash
./godl download \
  -workers 8 \
  -chunk-mib 8 \
  -parallel-min-mib 32 \
  -attempts 5 \
  -part-timeout 2m \
  -sha256 '<64-character-hex-digest>' \
  'https://example.com/large-file.bin' \
  './large-file.bin'
```

Repeat `-header` for request headers:

```bash
./godl download \
  -header 'Authorization: Bearer TOKEN' \
  -header 'X-Tenant-ID: tenant-a' \
  URL OUTPUT
```

For expiring signed URLs, use a stable resume identity:

```bash
./godl download -resume-key 'bucket/object/version-42' SIGNED_URL OUTPUT
```

The direct downloader writes `OUTPUT.part`, `OUTPUT.part.meta`, and `OUTPUT.lock` while a transfer is active. Ordinary failures and cancellation retain resumable state. A successful, verified transfer atomically renames the partial file to the destination and removes temporary metadata. `SIGINT` and `SIGTERM` cancel active HTTP requests.

## Background service and CLI client

Start the loopback service with a SQLite database:

```bash
./godl service \
  --address 127.0.0.1:51000 \
  --database "$HOME/Library/Application Support/godl/godl.db" \
  --max-concurrent 3
```

The service is intentionally bound to a loopback address. Use the client commands from another terminal:

```bash
./godl add 'https://example.com/file.bin' '/Users/me/Downloads/file.bin'
./godl list
./godl pause <download-id>
```

The CLI uses the service base URL `http://127.0.0.1:51000` by default. Each service command accepts `--address` to use another loopback port:

```bash
./godl list --address http://127.0.0.1:51001
```

The service persists download identity, source URL, destination, status, byte progress, errors, and timestamps in SQLite. The manager publishes lifecycle and progress events while the runner downloads the destination.

## HTTP API

The service exposes these loopback endpoints:

| Method | Path | Behavior |
| --- | --- | --- |
| `GET` | `/v1/ping` | Returns `{"status":"ok"}`. |
| `GET` | `/v1/downloads` | Lists persisted downloads ordered by creation time. |
| `POST` | `/v1/downloads` | Accepts `{"url":"...","destination":"..."}` and returns `202` with the queued item. |
| `POST` | `/v1/downloads/{id}:pause` | Cancels an active runner and marks the item paused. Returns `204`. |
| `GET` | `/v1/events` | Opens a `text/event-stream` progress feed. |

SSE events use the standard format:

```text
id: 12
event: progress
data: {"id":"...","status":"running","completed":123,"total":456}

```

The React frontend consumes this stream and reconnects after a disconnected service. M0 does not yet implement pairing tokens, origin allowlists, or authentication; the loopback boundary is the only service exposure control.

## Repository layout

```text
cmd/godl/             Cobra CLI: service, add, list, pause, download
downloader/           Resumable concurrent HTTP downloader
internal/download/    Shared queue model and event contract
internal/engine/      Service adapter for the downloader
internal/store/       SQLite persistence
internal/manager/     Queue scheduling and runner lifecycle
internal/api/         Loopback JSON and SSE HTTP API
internal/client/      Thin service client
internal/service/     Service composition and lifecycle
app/frontend/         Optional React queue table
DESIGN.md             Full M0–M7 product contract
```

## Current M0 scope

Implemented:

- downloader engine and direct CLI path;
- SQLite persistence with startup migration;
- ordered queue with configurable active-download limit;
- loopback JSON API and SSE progress feed;
- service client commands;
- native-utility React queue table with loading, empty, error, pause, and reconnect states.

Not implemented yet:

- dynamic interval segmentation and interval checkpoints;
- platform-specific physical preallocation;
- manager priorities, per-host caps, and rate limiting;
- Wails desktop shell integration;
- browser extension pairing and capture;
- encrypted cookies, keyring storage, structured redacted logs;
- HLS/DASH sources, ffmpeg remux, and M6 extras;
- release installers, package manifests, and update checks.

## Security and operational limits

- The service rejects non-loopback listen addresses, but it does not yet implement authentication or extension-origin authorization.
- URLs are validated for HTTP or HTTPS syntax; the service does not yet provide SSRF protection for hostile external callers. Do not expose the service beyond loopback.
- The destination lock is an exclusive lock file. A process killed with `SIGKILL` can leave a stale lock that must be removed only after confirming no downloader is running.
- The current partial-file setup uses `Truncate`; platform-specific physical preallocation is an M1 task.
- Do not log or share authorization headers, cookies, or signed URLs.

See [DESIGN.md](DESIGN.md) for the accepted tradeoffs, required fault-injection tests, platform tiers, external dependencies, legal artifacts, and known release blockers.
