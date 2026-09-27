#!/usr/bin/env bash
# Install-to-download check for a release on a clean Mac: installs godl.app
# from the .dmg, launches it, queues a download with the CLI through the
# app's embedded service, and verifies the file.
#
#   scripts/smoke-macos.sh godl-desktop_1.0.0_macos_universal.dmg path/to/godl-cli [appdir]
set -euo pipefail

dmg="$1"
cli="$2"
appdir="${3:-/Applications}"
work="$(mktemp -d)"
mount="$work/mnt"
cleanup() {
  pkill -f "$appdir/godl.app/Contents/MacOS/godl" 2>/dev/null || true
  if [ -n "${server:-}" ]; then kill "$server" 2>/dev/null || true; wait "$server" 2>/dev/null || true; fi
  hdiutil detach -quiet "$mount" 2>/dev/null || true
  rm -rf "$work"
}
diagnose() {
  echo "== diagnostics"
  "$cli" --token-file "$token" list 2>&1 || true
  tail -n 40 "$HOME/Library/Application Support/godl/godl.log" 2>/dev/null || true
}
trap 'status=$?; [ $status -ne 0 ] && [ -n "${token:-}" ] && diagnose; cleanup' EXIT

echo "== install"
mkdir -p "$mount"
hdiutil attach -quiet -nobrowse -mountpoint "$mount" "$dmg"
rm -rf "$appdir/godl.app"
cp -R "$mount/godl.app" "$appdir/"
hdiutil detach -quiet "$mount"
codesign --verify --deep "$appdir/godl.app"

echo "== serve a test file"
mkdir -p "$work/www"
head -c 5000000 /dev/urandom > "$work/www/payload.bin"
want="$(shasum -a 256 "$work/www/payload.bin" | cut -d' ' -f1)"
(cd "$work/www" && exec python3 -m http.server 18790 --bind 127.0.0.1 >/dev/null 2>&1) &
server=$!

echo "== launch"
"$appdir/godl.app/Contents/MacOS/godl" >"$work/app.log" 2>&1 &
token="$HOME/Library/Application Support/godl/token"
for _ in $(seq 1 150); do
  "$cli" --token-file "$token" list >/dev/null 2>&1 && break
  sleep 1
done
"$cli" --token-file "$token" list >/dev/null

echo "== download"
dest="$HOME/Downloads/godl-smoke-$$.bin"
"$cli" --token-file "$token" add "http://127.0.0.1:18790/payload.bin" "$dest" >/dev/null
for _ in $(seq 1 120); do
  [ -f "$dest" ] && break
  sleep 1
done
got="$(shasum -a 256 "$dest" | cut -d' ' -f1)"
[ "$got" = "$want" ] || { echo "checksum mismatch"; exit 1; }
# The attribute is set right after the file appears.
for _ in $(seq 1 10); do xattr -p com.apple.quarantine "$dest" >/dev/null 2>&1 && break; sleep 1; done
xattr -p com.apple.quarantine "$dest" >/dev/null
rm -f "$dest"
echo "install-to-download OK"
