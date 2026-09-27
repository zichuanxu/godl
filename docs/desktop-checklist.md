# Desktop manual checklist

Run on macOS and Windows before tagging a release that changes `app/`. Start
from a clean data directory (`~/Library/Application Support/godl` or
`%AppData%\godl`) and with no `godl service` running.

## Startup and shell

- [ ] The app opens a window; `godl list` from a terminal works while it runs (the loopback API is up).
- [ ] With `godl service` already running, the app shows the "could not start" banner instead of crashing.
- [ ] Launching the app a second time focuses the first window instead of starting another.
- [ ] Closing the window hides it; downloads continue. The tray (menu bar) icon shows it again.
- [ ] Tray menu: Pause all, Resume all, and Quit work. Quit lets running downloads checkpoint; the next start resumes them.
- [ ] On macOS, clicking the Dock icon with the window hidden shows it.

## Adding downloads

- [ ] Add a URL: the file name comes from the server and lands in the category folder (for example `Downloads/Documents`).
- [ ] Add two URLs at once (one per line): both are queued.
- [ ] Choose a folder: the download goes there; the folder is listed under Settings → Desktop.
- [ ] Give a file name: it is used as is.
- [ ] Drag a link from a browser onto the window: the add dialog opens with the URL.
- [ ] Paste a browser "Copy as cURL" command (bash and cmd variants): the URL and the cookie/referer headers are imported.
- [ ] Copy a URL, then a cURL command, in another app: the clipboard offer appears; Download opens the add dialog; Dismiss hides it. Turning the monitor off in Settings stops the offers.

## Queue

- [ ] Progress bar, speed, time left, and the status bar sparkline update while downloading.
- [ ] Pause, Resume, Retry, and Delete (with and without "also delete the file") behave as labelled.
- [ ] Open launches a completed file; Show in folder reveals it in Finder or Explorer.
- [ ] A download that fails with 401 or 403 offers Re-authenticate; pasting a fresh cURL command retries it.
- [ ] A high-priority download starts before normal ones when the queue is full.

## Settings

- [ ] Downloads at once, connections, per-server connections, and total speed limit take effect after Save.
- [ ] A queue window that excludes the current time stops running downloads; removing it restarts them.
- [ ] Time-of-day speed rules change the total speed.
- [ ] Proxy: System, No proxy, and Manual (with a local proxy) work; invalid values show an error.
- [ ] Site settings JSON with a password: the password shows as `********` after reopening Settings and still works.
- [ ] Notifications arrive for completed and failed downloads (macOS: from the `.app` bundle only), and stop when turned off.
- [ ] "Always ask where to save" opens the folder picker on Add.
- [ ] "Start godl when I log in" registers the app (check after logging out and in).

## Secrets

- [ ] After adding a download with a cookie, the cookie value does not appear in `godl.db` (for example `strings godl.db | grep <value>` finds nothing).
