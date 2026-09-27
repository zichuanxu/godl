#!/usr/bin/env bash
# Builds NimGet.app as a universal (arm64 + x86_64) binary, signs it ad hoc
# (NimGet is unsigned; see DESIGN.md section 10), and packs it as a .dmg and a
# .zip. Run from app/ after `npm run build` in app/frontend.
#
#   scripts/package-macos.sh 1.0.0 dist
set -euo pipefail

version="${1:?version, for example 1.0.0}"
out="${2:?output directory}"
mkdir -p "$out"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

export CGO_ENABLED=1 MACOSX_DEPLOYMENT_TARGET=12.0
export CGO_CFLAGS=-mmacosx-version-min=12.0 CGO_LDFLAGS=-mmacosx-version-min=12.0
ldflags="-s -w -X main.version=v${version}"
for arch in arm64 amd64; do
  GOARCH="$arch" go build -tags production -trimpath -ldflags "$ldflags" -o "$work/nimget-$arch" .
done

app="$work/NimGet.app/Contents"
mkdir -p "$app/MacOS" "$app/Resources"
lipo -create -output "$app/MacOS/nimget" "$work/nimget-arm64" "$work/nimget-amd64"
cp build/darwin/icons.icns "$app/Resources/icons.icns"
# CFBundleVersion is numeric (1.0.0-rc.1 becomes 1.0.0); the short version
# string shows the full release name.
plutil -convert xml1 -o "$app/Info.plist" build/darwin/Info.plist
plutil -replace CFBundleVersion -string "${version%%-*}" "$app/Info.plist"
plutil -replace CFBundleShortVersionString -string "$version" "$app/Info.plist"
codesign --force --deep --sign - "$work/NimGet.app"
codesign --verify --deep "$work/NimGet.app"

name="nimget-desktop_${version}_macos_universal"
ditto -c -k --sequesterRsrc --keepParent "$work/NimGet.app" "$out/$name.zip"

staging="$work/dmg"
mkdir -p "$staging"
cp -R "$work/NimGet.app" "$staging/"
ln -s /Applications "$staging/Applications"
hdiutil create -quiet -volname "NimGet ${version}" -srcfolder "$staging" -fs HFS+ -format UDZO -ov "$out/$name.dmg"
ls -l "$out"
