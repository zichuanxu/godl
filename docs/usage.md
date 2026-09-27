# Using godl

The CLI, the background service, its settings and HTTP API, and the security model.

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
