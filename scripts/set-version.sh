#!/usr/bin/env bash
# Writes a release version into every build file that carries one: build/config.yml, the macOS Info.plist files, the
# Windows manifest, info.json, MSIX files and NSIS defaults, and the deb's nfpm.yaml. The files are committed with the
# development version 0.0.0; the app itself gets its version from -ldflags "-X main.version=...".
# Usage: scripts/set-version.sh VERSION   (such as v0.1.0 or 0.1.0-3-gabc1234; only X.Y.Z goes into the files)
# Running it again with the same version changes nothing, as the build tasks do once per architecture.
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
raw=${1:?usage: set-version.sh VERSION}
num=$(printf '%s\n' "$raw" | sed -nE 's/^v?([0-9]+\.[0-9]+\.[0-9]+).*/\1/p')
[ -n "$num" ] || { echo "set-version: '$raw' does not start with X.Y.Z" >&2; exit 1; }

# edit FILE SED-SCRIPT...: writes the version (@V@ in the scripts) into FILE. The scripts must match, which a probe
# with a version no file holds proves, so a changed template fails even when FILE already holds this version.
edit() {
  local rel=$1 file=$root/$1; shift
  [ -f "$file" ] || { echo "set-version: $rel is missing" >&2; exit 1; }
  local probe=("${@//@V@/9999.9999.9999}") real=("${@//@V@/$num}")
  if sed -E "${probe[@]}" "$file" | cmp -s - "$file"; then
    echo "set-version: no version to replace in $rel" >&2
    exit 1
  fi
  sed -E "${real[@]}" "$file" > "$file.tmp"
  cat "$file.tmp" > "$file" && rm "$file.tmp"
}

edit build/config.yml -e 's/^(  version: ")[^"]*"/\1@V@"/'
for plist in build/darwin/Info.plist build/darwin/Info.dev.plist; do
  for key in CFBundleVersion CFBundleShortVersionString; do
    edit "$plist" -e "/<key>$key<\/key>/{" -e 'n' -e 's|<string>[^<]*</string>|<string>@V@</string>|' -e '}'
  done
done
edit build/windows/info.json -e 's/("file_version": ")[^"]*"/\1@V@"/'
edit build/windows/info.json -e 's/("ProductVersion": ")[^"]*"/\1@V@"/'
for manifest in "$root"/build/windows/*.exe.manifest; do
  edit "${manifest#"$root"/}" -e '/name="dev\.ciranda\./s/version="[^"]*"/version="@V@.0"/'
done
edit build/windows/msix/app_manifest.xml -e 's/^( *Version=")[^"]*"/\1@V@.0"/'
edit build/windows/msix/template.xml -e 's/^( *Version=")[^"]*"/\1@V@.0"/'
edit build/windows/nsis/wails_tools.nsh -e 's/(!define INFO_PRODUCTVERSION ")[^"]*"/\1@V@"/'
edit build/linux/nfpm/nfpm.yaml -e 's/^(version: ")[^"]*"/\1@V@"/'
edit build/windows/nsis/project.nsi -e '/INFO_PRODUCTVERSION/s/Default "[^"]*"/Default "@V@"/'
