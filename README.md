# godl

`godl` is a fast, cross-platform download manager written in Go. It has two surfaces today:

- a direct command-line downloader for one-off transfers;
- a loopback service with a SQLite-backed queue, an authenticated JSON API, SSE progress events, and CLI client commands.

The downloader supports capability probing, validated byte ranges, bounded concurrency, retries, checkpoints, resume, SHA-256 verification, destination locking, and safe finalization.

The roadmap and design contract live in [DESIGN.md](DESIGN.md). This is milestone **M0.5 (hardening)**; the engine rewrite (M1), scheduler features (M2), and desktop GUI (M3) are not shipped yet.

## Requirements

- Go 1.25 or newer.
- Node.js and npm only for the optional React frontend under `app/frontend`.

## Build and test

```bash
go vet ./...
go test -race ./...
go build -o godl ./cmd/godl
./godl version
```

`go test -short ./...` skips the end-to-end crash-resume test, which takes several seconds.

## Direct downloader

The `download` command runs the downloader without the service:

```bash
./godl download \
  --workers 8 \
  --chunk-mib 8 \
  --parallel-min-mib 32 \
  --attempts 5 \
  --part-timeout 2m \
  --sha256 '<64-character-hex-digest>' \
  'https://example.com/large-file.bin' \
  './large-file.bin'
```

Repeat `--header` for request headers:

```bash
./godl download \
  --header 'Authorization: Bearer TOKEN' \
  --header 'X-Tenant-ID: tenant-a' \
  URL OUTPUT
```

For expiring signed URLs, use a stable resume identity:

```bash
./godl download --resume-key 'bucket/object/version-42' SIGNED_URL OUTPUT
```

While a transfer is active the downloader keeps `OUTPUT.part`, `OUTPUT.part.meta`, and `OUTPUT.lock`. The lock is an OS advisory lock, so a crashed or killed process never leaves a lock that blocks the next run. Parallel transfers keep resumable state after failures and cancellation; single-stream transfers (no range support, no strong ETag, unknown size, or below `--parallel-min-mib`) restart from the beginning. A verified transfer is atomically renamed to the destination. `SIGINT` and `SIGTERM` cancel active requests.

Known limit until M1: `--part-timeout` bounds a whole range request, so on a very slow server one 8 MiB chunk, or a whole single-stream transfer, can exceed it.

## Background service and CLI client

```bash
./godl service \
  --listen 127.0.0.1:51000 \
  --download-root "$HOME/Downloads" \
  --max-concurrent 3
```

| Flag | Default | Meaning |
| --- | --- | --- |
| `--listen` | `127.0.0.1:51000` | Loopback address; non-loopback addresses are rejected. |
| `--database` | `<config dir>/godl/godl.db` | SQLite queue. |
| `--download-root` | `~/Downloads` | Directory downloads may be written under. Repeatable. |
| `--max-concurrent` | `3` | Active downloads. |
| `--log-level` | `info` | JSON logs on stderr: `debug`, `info`, `warn`, `error`. |
| `--token-file` | `<config dir>/godl/token` | API token, created on first start with owner-only permissions. |

`<config dir>` is `~/Library/Application Support` on macOS, `%AppData%` on Windows, and `$XDG_CONFIG_HOME` or `~/.config` on Linux.

Client commands read the same token file and talk to `http://127.0.0.1:51000` unless `--address` or `--token-file` say otherwise:

```bash
./godl add 'https://example.com/file.bin' ~/Downloads/file.bin
./godl list
./godl pause <id>
./godl resume <id>
./godl retry <id>
./godl delete <id> --files
```

Relative `OUTPUT` paths are resolved against the client's working directory. On restart, the service resumes downloads that were running when it stopped, including after a crash.

## HTTP API

Every endpoint except `/v1/ping` requires `Authorization: Bearer <token>`. Commands other than `GET` require `Content-Type: application/json`.

| Method | Path | Behavior |
| --- | --- | --- |
| `GET` | `/v1/ping` | `{"status":"ok"}` |
| `GET` | `/v1/downloads` | Lists downloads with live progress. |
| `POST` | `/v1/downloads` | Body `{"url":"...","destination":"/absolute/path"}`; returns `202` with the queued item. |
| `POST` | `/v1/downloads/{id}:pause` | Pauses a queued or running download. `204` |
| `POST` | `/v1/downloads/{id}:resume` | Requeues a paused download. `204` |
| `POST` | `/v1/downloads/{id}:retry` | Requeues a failed download. `204` |
| `DELETE` | `/v1/downloads/{id}` | Deletes a stopped download; `?files=true` also removes partial data and, for completed downloads, the file. `204` |
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

The React queue table in `app/frontend` predates these rules and cannot talk to the secured API from a browser; the M3 desktop shell replaces it with in-process bindings.

## Repository layout

```text
cmd/godl/             Cobra CLI: service, add, list, pause, resume, retry, delete, download, version
downloader/           Current resumable concurrent HTTP downloader (replaced in M1)
internal/download/    Shared queue model, errors, and event contract
internal/engine/      Runner adapter plus unwired M1 building blocks
internal/filelock/    OS advisory file locks
internal/logging/     slog setup and URL redaction
internal/store/       SQLite persistence
internal/manager/     Queue lifecycle, recovery, destination confinement, unwired M2 pieces
internal/api/         Authenticated loopback JSON and SSE API
internal/client/      Service client
internal/service/     Service composition, lifecycle, paths, and token
app/                  Desktop shell placeholder and React frontend
DESIGN.md             Design contract and M0–M6 roadmap
```

## License

Apache-2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE).
