# Installing NimGet

NimGet and its `nimget` CLI come from the same [release page](https://github.com/zichuanxu/nimget/releases/latest):

- **Desktop app** (macOS and Windows): the download manager with a window and a tray icon. It includes the service, so the CLI can talk to it.
- **CLI and service** (macOS, Windows, Linux): the `nimget` command, which downloads directly or runs a headless service.

NimGet is free and its builds are **not signed** (no paid Apple or Microsoft certificates). The first launch therefore needs one extra confirmation, described below.

## macOS (12 Monterey or later, Apple silicon and Intel)

### Desktop app

With Homebrew:

```bash
brew install --cask zichuanxu/tap/nimget-desktop
```

Or download `nimget-desktop_<version>_macos_universal.dmg`, open it, and drag **NimGet** to **Applications**.

On first launch macOS reports that it cannot verify the developer. Either:

- open **System Settings → Privacy & Security**, scroll to the message about NimGet, and choose **Open Anyway**; or
- run `xattr -dr com.apple.quarantine /Applications/NimGet.app` once.

NimGet lives in the menu bar; closing the window keeps downloads running. Choose **Quit NimGet** from the menu bar icon to stop it.

### CLI

```bash
brew install zichuanxu/tap/nimget
```

Or unpack `nimget_<version>_darwin_arm64.tar.gz` (Apple silicon) or `..._darwin_amd64.tar.gz` (Intel) and put `nimget` on your `PATH`. If macOS blocks the binary, run `xattr -d com.apple.quarantine nimget`.

## Windows (10 and 11, x64)

### Desktop app

With Scoop:

```powershell
scoop bucket add zichuanxu https://github.com/zichuanxu/scoop-bucket
scoop install zichuanxu/nimget-desktop
```

Or download `nimget-desktop_<version>_windows_amd64_setup.exe` (installer, no administrator rights needed) or `..._windows_amd64.zip` (portable).

SmartScreen may show "Windows protected your PC". Choose **More info → Run anyway**. The app needs the Microsoft Edge WebView2 Runtime, which Windows 11 includes and Windows 10 receives through Windows Update; the installer offers the download page if it is missing.

The installer puts NimGet in `%LocalAppData%\Programs\nimget` and adds it to the Start menu and to **Settings → Apps**, where it can be uninstalled. Your downloads, settings, and logs (`%AppData%\nimget`) are kept.

### CLI

```powershell
scoop bucket add zichuanxu https://github.com/zichuanxu/scoop-bucket
scoop install zichuanxu/nimget
```

or unpack `nimget_<version>_windows_amd64.zip` and put `nimget.exe` on your `PATH`. Do not run `nimget service` while the desktop app is open: both use the same port.

## Linux (x86-64 and arm64)

The desktop app is not shipped for Linux. Install the CLI with Homebrew on Linux (`brew install zichuanxu/tap/nimget`) or unpack `nimget_<version>_linux_amd64.tar.gz` or `..._linux_arm64.tar.gz`, then:

```bash
nimget download https://example.com/file.iso ./file.iso   # one-off download
nimget service &                                          # headless queue
nimget add https://example.com/file.iso
```

Without a desktop keyring (a typical server), cookies and passwords you give nimget are kept in memory only and must be supplied again after a restart.

## Verifying downloads

Every release lists SHA-256 checksums in `checksums.txt` (CLI archives) and `desktop-checksums.txt` (desktop packages):

```bash
shasum -a 256 -c checksums.txt --ignore-missing
```

## Updates

The desktop app checks GitHub once a week and shows a banner when a newer version exists (turn this off in **Settings → Desktop**). The check goes through the proxy configured in nimget. It never downloads or installs anything by itself: update with the same method you installed with (`brew upgrade`, `scoop update`, or a new download).

## Uninstalling

- macOS: `brew uninstall --cask nimget-desktop`, or drag NimGet from Applications to the Trash. Remove `~/Library/Application Support/nimget` to delete settings and the queue.
- Windows: **Settings → Apps → NimGet → Uninstall** or `scoop uninstall nimget-desktop`. Remove `%AppData%\nimget` to delete settings and the queue.
