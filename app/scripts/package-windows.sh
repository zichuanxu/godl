#!/usr/bin/env bash
# Builds nimget.exe with its icon and manifest, a portable .zip, and a
# per-user NSIS installer. Run from app/ in Git Bash after `npm run build` in
# app/frontend; needs wails3 (for the resource file) and makensis.
#
#   scripts/package-windows.sh 1.0.0 dist
set -euo pipefail

version="${1:?version, for example 1.0.0}"
out="${2:?output directory}"
mkdir -p "$out"
work="$(mktemp -d)"
trap 'rm -rf "$work" wails_windows_amd64.syso' EXIT

# Windows version fields are numeric only: 1.0.0-rc.1 becomes 1.0.0.
numeric="${version%%-*}"

# Version resource, icon, and manifest (DPI awareness, common controls v6).
sed -e "s/\"file_version\": \"0\.0\.0\"/\"file_version\": \"${numeric}\"/" -e "s/0\.0\.0/${version}/g" build/windows/info.json > "$work/info.json"
wails3 generate syso -arch amd64 -icon build/windows/icon.ico \
  -manifest build/windows/wails.exe.manifest -info "$work/info.json" -out wails_windows_amd64.syso

GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -tags production -trimpath \
  -ldflags "-s -w -H windowsgui -X main.version=v${version}" -o "$work/nimget.exe" .

name="nimget-desktop_${version}_windows_amd64"
mkdir -p "$work/portable"
cp "$work/nimget.exe" ../LICENSE ../NOTICE "$work/portable/"
(cd "$work/portable" && 7z a -tzip -bso0 "$work/$name.zip" .)
cp "$work/$name.zip" "$out/"

# makensis resolves relative paths against the script's directory, so every
# path it gets is absolute.
here="$(pwd)"
mkdir -p "$out" && out="$(cd "$out" && pwd)"
makensis -V2 -DVERSION="${version}" -DNUMVERSION="${numeric}" -DBINARY="$(cygpath -w "$work/nimget.exe")" \
  -DLICENSE="$(cygpath -w "$here/../LICENSE")" -DICON="$(cygpath -w "$here/build/windows/icon.ico")" \
  -DOUTFILE="$(cygpath -w "$out/nimget-desktop_${version}_windows_amd64_setup.exe")" \
  "$(cygpath -w "$here/build/windows/installer.nsi")"
ls -l "$out"
