#!/usr/bin/env bash
# Install-to-download check for a release on a clean Windows machine (Git
# Bash): runs the installer silently, launches the installed app, queues a
# download with the CLI through the app's embedded service, verifies the file,
# and uninstalls.
#
#   scripts/smoke-windows.sh godl-desktop_1.0.0_windows_amd64_setup.exe path/to/godl.exe
set -euo pipefail

setup="$1"
cli="$2"
work="$(mktemp -d)"
installdir="$LOCALAPPDATA/Programs/godl"
cleanup() {
  taskkill //IM godl.exe //F >/dev/null 2>&1 || true
  [ -n "${server:-}" ] && kill "$server" 2>/dev/null || true
  rm -rf "$work"
}
trap cleanup EXIT

echo "== install"
cmd //c "$(cygpath -w "$setup") /S"
for _ in $(seq 1 30); do [ -f "$installdir/godl.exe" ] && break; sleep 1; done
[ -f "$installdir/godl.exe" ] || { echo "not installed"; exit 1; }
powershell -NoProfile -Command "Get-ItemProperty 'HKCU:\\Software\\Microsoft\\Windows\\CurrentVersion\\Uninstall\\godl' | Select-Object DisplayName, DisplayVersion"

echo "== serve a test file"
mkdir -p "$work/www"
head -c 5000000 /dev/urandom > "$work/www/payload.bin"
want="$(sha256sum "$work/www/payload.bin" | cut -d' ' -f1)"
(cd "$work/www" && exec python -m http.server 18790 --bind 127.0.0.1 >/dev/null 2>&1) &
server=$!

echo "== launch"
powershell -NoProfile -Command "Start-Process -FilePath '$(cygpath -w "$installdir/godl.exe")'"
token="$APPDATA/godl/token"
for _ in $(seq 1 150); do
  "$cli" --token-file "$token" list >/dev/null 2>&1 && break
  sleep 1
done
"$cli" --token-file "$token" list >/dev/null

echo "== download"
dest="$(cygpath -w "$USERPROFILE/Downloads/godl-smoke-$$.bin")"
"$cli" --token-file "$token" add "http://127.0.0.1:18790/payload.bin" "$dest" >/dev/null
for _ in $(seq 1 60); do [ -f "$dest" ] && break; sleep 1; done
got="$(sha256sum "$dest" | cut -d' ' -f1)"
[ "$got" = "$want" ] || { echo "checksum mismatch"; exit 1; }
powershell -NoProfile -Command "Get-Content -Path '$dest' -Stream Zone.Identifier" | grep -q ZoneId=3
rm -f "$dest"

echo "== uninstall"
taskkill //IM godl.exe //F >/dev/null 2>&1 || true
cmd //c "$(cygpath -w "$installdir/uninstall.exe") /S"
for _ in $(seq 1 30); do [ ! -f "$installdir/godl.exe" ] && break; sleep 1; done
[ ! -f "$installdir/godl.exe" ] || { echo "not uninstalled"; exit 1; }
echo "install-to-download OK"
