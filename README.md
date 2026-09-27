# godl

`godl` is a fast, cross-platform download manager written in Go. It has two surfaces today:

- a direct command-line downloader for one-off transfers;
- a loopback service with a SQLite-backed queue, an authenticated JSON API, SSE progress events, and CLI client commands.

The engine splits a download across up to 32 HTTP/1.1 connections and rebalances them continuously: when a connection finishes, it takes over half of the largest remaining range, and connections far slower than the rest are replaced. Progress is checkpointed at byte granularity, so an interrupted download resumes where it stopped, even after a crash. Responses are validated against the probed ETag or Last-Modified, `Content-Range`, and size, and an optional checksum is verified before the file is committed atomically.

The roadmap and design contract live in [DESIGN.md](DESIGN.md). This is milestone **M1 (engine)**; scheduler features (M2) and the desktop GUI (M3) are not shipped yet.

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

`go test -short ./...` skips the slower end-to-end tests.

The performance gate compares godl with `aria2c -x16 -s16` on a local server that caps each connection's bandwidth (CI runs it on every push):

```bash
go build -o godl ./cmd/godl
go run ./tools/bench -godl ./godl -gate
```

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
| `--overwrite` | off | Replace an existing destination. |

```bash
./godl download --header 'Authorization: Bearer TOKEN' URL OUTPUT
./godl download --resume-key 'bucket/object/version-42' SIGNED_URL OUTPUT
```

While a transfer is active the engine keeps `OUTPUT.part`, `OUTPUT.part.meta`, and `OUTPUT.lock`. The lock is an OS advisory lock, so a crashed or killed process never leaves a lock that blocks the next run. The engine checks free space and preallocates the file before downloading.

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
internal/download/    Shared queue model, errors, and event contract
internal/engine/      Download engine: work-stealing ranges, checkpoints, validation
internal/filelock/    OS advisory file locks
internal/logging/     slog setup and URL redaction
internal/store/       SQLite persistence
internal/manager/     Queue lifecycle, recovery, destination confinement, unwired M2 pieces
internal/api/         Authenticated loopback JSON and SSE API
internal/client/      Service client
internal/service/     Service composition, lifecycle, paths, and token
tools/bench/          Performance gate against aria2c
app/                  Desktop shell placeholder and React frontend
DESIGN.md             Design contract and M0–M6 roadmap
```

## License

Apache-2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE).
