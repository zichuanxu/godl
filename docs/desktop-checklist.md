# Desktop manual checklist

Run on macOS and Windows before tagging a release that changes `app/`. Start
from a clean data directory (`~/Library/Application Support/nimget` or
`%AppData%\nimget`) and with no `nimget service` running.

## Startup and shell

- [ ] The app opens a window; `nimget list` from a terminal works while it runs (the loopback API is up).
- [ ] With `nimget service` already running, the app shows the "could not start" banner instead of crashing.
- [ ] Launching the app a second time focuses the first window instead of starting another.
- [ ] Closing the window hides it; downloads continue. The tray (menu bar) icon shows it again.
- [ ] Tray menu: Pause all, Resume all, and Quit work. Quit lets running downloads checkpoint; the next start resumes them.
- [ ] On macOS, clicking the Dock icon with the window hidden shows it.
- [ ] On macOS the sidebar is translucent and the window can be dragged by the header and the sidebar top; buttons and the search box there still respond to clicks.
- [ ] Settings → General → Language: English and 简体中文 switch every label, the tray menu, and notifications; "System" follows the OS language.
- [ ] Settings → General → Appearance: System, Light, and Dark apply at once and survive a restart.
- [ ] NimGet's icon is legible beside the sidebar title in both Light and Dark appearances.

## Adding downloads

- [ ] Add a URL: the file name comes from the server and lands in the category folder (for example `Downloads/Documents`).
- [ ] Add two URLs at once (one per line): both are queued.
- [ ] Choose a folder: the download goes there; the folder is listed under Settings → Advanced.
- [ ] Give a file name: it is used as is.
- [ ] Drag a link from a browser onto the window: the add dialog opens with the URL.
- [ ] Paste a browser "Copy as cURL" command (bash and cmd variants): the URL and the cookie/referer headers are imported.
- [ ] Copy a URL, then a cURL command, in another app: the clipboard offer appears; Download opens the add dialog; Dismiss hides it. Turning the monitor off in Settings stops the offers.

## Queue

- [ ] Progress bar, speed, time left, and the sidebar speed card update while downloading.
- [ ] Sidebar filters (Active, Paused, Completed, Failed) and their counts match the list; search filters by name and URL.
- [ ] Hovering a row shows its quick actions; right-click and the ⋯ button open the same actions menu.
- [ ] Keyboard: ⌘/Ctrl+N new download, ⌘/Ctrl+, settings, ⌘/Ctrl+F search, arrows select, Space pauses or resumes, Enter opens, Delete asks to delete, Esc closes dialogs.
- [ ] Pause, Resume, Retry, and Delete (with and without "also delete the file") behave as labelled.
- [ ] The top Resume all button is disabled without paused downloads; Pause all is disabled without queued or running downloads. In a mixed queue, both are available.
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
- [ ] "Start NimGet when I log in" registers the app (check after logging out and in).

## Extras (M4)

- [ ] Add `https://…/img[001-005].jpg`: the dialog shows "Batch pattern: 5 URLs" and queues five downloads.
- [ ] Export writes a JSON file without cookies; Import of that file, and of a text file of URLs, queues them again.
- [ ] Settings → Downloads → "When the queue finishes: Sleep" with a short download: a 30 s countdown appears when the queue empties; Cancel stops it; letting it run puts the computer to sleep once, and the choice resets to Nothing.
- [ ] Settings → Downloads → "When the queue finishes: Shut down" shows the same countdown (cancel it, or test on a VM).
- [ ] "Open files when they finish" opens each completed file.

## HLS (M5)

- [ ] Add a VOD `.m3u8` URL: it downloads as one `.ts` file in `Video` that plays in VLC or QuickTime (via MP4 conversion).
- [ ] An AES-128 encrypted stream plays after download.
- [ ] Pause and resume an HLS download: it continues from the finished segments.
- [ ] With ffmpeg installed, "Convert to MP4" creates a playable `.mp4` next to the `.ts`; without ffmpeg the button is hidden.

## Release (M6)

- [ ] The dmg opens with NimGet.app and an Applications link; after "Open Anyway" the app starts and shows the NimGet icon in the Dock and the menu bar.
- [ ] The Windows installer installs without administrator rights, adds a Start menu entry and an Apps entry, and uninstalls cleanly (settings in `%AppData%\nimget` are kept).
- [ ] With an older version installed, the update banner appears within a minute of launch (or after the weekly check), links to the release, and Dismiss hides it for that version.

## Secrets

- [ ] After adding a download with a cookie, the cookie value does not appear in `nimget.db` (for example `strings nimget.db | grep <value>` finds nothing).
