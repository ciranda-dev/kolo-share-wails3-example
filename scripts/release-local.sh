#!/usr/bin/env bash
# Builds the release files of a Wails 3 app on a macOS host, without CI:
#   macOS universal .app (.zip, .dmg), Windows amd64 and arm64 (.exe, .zip, NSIS installer),
#   Linux amd64 and arm64 built in Docker (.tar.gz, .deb), and SHA256SUMS.
# Usage: scripts/release-local.sh [output dir]   (default ./dist-release; files go to <dir>/<version>)
# Needs Go, Node (npm), the wails3 CLI and Docker Desktop. The build runs in a temporary copy of HEAD.
# The version is the git tag: it goes into the app with -ldflags "-X main.version=" and, with scripts/set-version.sh,
# into the bundle, the installers and the deb.
set -euo pipefail

APP_DIR=${APP_DIR:-$(cd "$(dirname "$0")/.." && pwd)}
APP_NAME=${APP_NAME:-$(sed -n 's/^ *APP_NAME: *"\(.*\)"/\1/p' "$APP_DIR/Taskfile.yml" | head -1)}
VERSION=${VERSION:-$(git -C "$APP_DIR" describe --tags --dirty)}
OUT=${1:-$APP_DIR/dist-release}
WINDOWS_ARCHES=${WINDOWS_ARCHES:-amd64 arm64}
LINUX_ARCHES=${LINUX_ARCHES:-arm64 amd64}
DOCKER_CPUS=${DOCKER_CPUS:-3}
# Ubuntu 24.04 (index digest), which ships the GTK 4 and WebKitGTK 6.0 that Wails 3 links on Linux.
BASE_IMAGE=${BASE_IMAGE:-ubuntu@sha256:2198e4c65076e9cb0b3d695219df667c0e3b0072da64ca3c2648aca17dcecaf5}
GO_VERSION=${GO_VERSION:-$(go env GOVERSION)}
KEEP_IMAGES=${KEEP_IMAGES:-0}

ver=${VERSION#v}
# KOLO_VERSION is the version the Taskfile builds in (the copy of HEAD below has no .git to ask).
export GOWORK=off COPYFILE_DISABLE=1 KOLO_VERSION=$ver
name=$APP_NAME-$VERSION
# hdiutil fails and zip appends when a target exists, so a second run into the same folder would mix two builds.
if [ -n "$(ls -A "$OUT/$VERSION" 2>/dev/null)" ]; then
  echo "release-local: $OUT/$VERSION is not empty; remove it or pick another output dir" >&2
  exit 1
fi
# The build uses a copy of HEAD; with KOLO_RELEASE_ALLOW_DIRTY=1 it copies the tracked files with their changes.
if [ -n "$(git -C "$APP_DIR" status --porcelain)" ]; then
  echo "release-local: warning: $APP_DIR has uncommitted changes" >&2
  [ "${KOLO_RELEASE_ALLOW_DIRTY:-0}" = 1 ] || { echo "release-local: commit them, or set KOLO_RELEASE_ALLOW_DIRTY=1 to build them" >&2; exit 1; }
fi
mkdir -p "$OUT/$VERSION"
out=$(cd "$OUT/$VERSION" && pwd)
work=$(mktemp -d)
src=$work/src
prep=kolo-release-prep-$$
trap 'rm -rf "$work"; docker rm -f "$prep" >/dev/null 2>&1 || true' EXIT
mkdir "$src"
tree=$(git -C "$APP_DIR" stash create) # a commit of the tracked changes, none when there are none
git -C "$APP_DIR" archive "${tree:-HEAD}" | tar -x -C "$src"
cd "$src"
scripts/set-version.sh "$ver"

# image TAG ARCH PACKAGES [SETUP]: an image from BASE_IMAGE with apt PACKAGES, built once with docker run (so --cpus applies).
image() {
  docker image inspect "$1" >/dev/null 2>&1 && return
  docker run --name "$prep" --platform "linux/$2" --cpus "$DOCKER_CPUS" "$BASE_IMAGE" bash -euc "
    export DEBIAN_FRONTEND=noninteractive
    apt-get update -q && apt-get install -yq --no-install-recommends $3
    ${4:-}
    apt-get clean && rm -rf /var/lib/apt/lists/*"
  docker commit "$prep" "$1" >/dev/null
  docker rm "$prep" >/dev/null
  docker image rm "$BASE_IMAGE" >/dev/null # the classic image store keeps one platform per reference
}

# carries BINARY: whether BINARY holds $ver as a string of its own, not only inside a module version v$ver. The build
# info that go version -m prints has no -ldflags under -trimpath, so the binary itself is searched.
carries() { LC_ALL=C grep -aoE -- "(^|.)${ver//./\\.}" "$1" | grep -v '^v' >/dev/null; }

# run_in IMAGE ARCH CMD...: runs CMD in the build copy.
run_in() {
  local img=$1 arch=$2; shift 2
  docker run --rm --platform "linux/$arch" --cpus "$DOCKER_CPUS" -v "$src:/src" -w /src \
    -e GOFLAGS=-mod=vendor -e GOTOOLCHAIN=local -e CGO_ENABLED=1 -e GOMAXPROCS="$DOCKER_CPUS" "$img" "$@"
}

# macOS: the Taskfile builds the frontend once, both architectures, lipo and the ad-hoc signed bundle.
wails3 task darwin:package:universal APP_VERSION="$ver"
rm -f "$out/$name-macos-universal.zip" "$out/$name-macos-universal.dmg"
ditto -c -k --norsrc --noextattr --noqtn --keepParent "bin/$APP_NAME.app" "$out/$name-macos-universal.zip"
mkdir "$work/dmg"
cp -R "bin/$APP_NAME.app" "$work/dmg/"
ln -s /Applications "$work/dmg/Applications"
[ "$(plutil -extract CFBundleShortVersionString raw "bin/$APP_NAME.app/Contents/Info.plist")" = "${ver%%[!0-9.]*}" ] ||
  { echo "release-local: the bundle does not carry version $ver" >&2; exit 1; }
carries "bin/$APP_NAME.app/Contents/MacOS/$APP_NAME" || { echo "release-local: the macOS build does not carry version $ver" >&2; exit 1; }
hdiutil create -quiet -volname "$name" -srcfolder "$work/dmg" -format UDZO "$out/$name-macos-universal.dmg"

# Windows: pure Go cross-compilation, the installer from the project's NSIS script.
wails3 generate webview2bootstrapper -dir build/windows/nsis
image kolo-release-nsis:arm64 arm64 nsis
for arch in $WINDOWS_ARCHES; do
  wails3 task windows:build ARCH="$arch" APP_VERSION="$ver"
  carries "bin/$APP_NAME.exe" || { echo "release-local: the Windows $arch build does not carry version $ver" >&2; exit 1; }
  cp "bin/$APP_NAME.exe" "$out/$name-windows-$arch.exe"
  rm -f "$out/$name-windows-$arch.zip"
  (cd bin && zip -q "$out/$name-windows-$arch.zip" "$APP_NAME.exe")
  run_in kolo-release-nsis:arm64 arm64 bash -c "cd build/windows/nsis && makensis -V2 \
    -DARG_WAILS_$(echo "$arch" | tr a-z A-Z)_BINARY=/src/bin/$APP_NAME.exe project.nsi"
  mv "bin/$APP_NAME-$arch-installer.exe" "$out/$name-windows-$arch-installer.exe"
done

# Linux: cgo against GTK 4 and WebKitGTK 6.0, in a native or emulated container per architecture.
go mod vendor
for arch in $LINUX_ARCHES; do
  img=kolo-release-linux:$arch-$GO_VERSION
  image "$img" "$arch" "ca-certificates curl gcc libc6-dev pkg-config libgtk-4-dev libwebkitgtk-6.0-dev" "
    f=$GO_VERSION.linux-$arch.tar.gz
    curl -fsSLO https://dl.google.com/go/\$f
    echo \"\$(curl -fsSL https://dl.google.com/go/\$f.sha256)  \$f\" | sha256sum -c -
    tar -C /usr/local -xzf \$f && rm \$f"
  rm -f "bin/$APP_NAME"
  run_in "$img" "$arch" /usr/local/go/bin/go build -tags production -trimpath -buildvcs=false \
    -ldflags="-w -s -X main.version=$ver" -o "bin/$APP_NAME" .
  carries "bin/$APP_NAME" || { echo "release-local: the Linux $arch build does not carry version $ver" >&2; exit 1; }
  d=$work/$name-linux-$arch
  mkdir "$d"
  cp "bin/$APP_NAME" "build/linux/$APP_NAME.desktop" "$d/"
  cp build/appicon.png "$d/$APP_NAME.png"
  tar -czf "$out/$name-linux-$arch.tar.gz" --no-xattrs --no-mac-metadata --uid 0 --gid 0 -C "$work" "$name-linux-$arch"
  GOARCH=$arch wails3 task linux:generate:deb
  mv bin/*.deb "$out/$name-linux-$arch.deb"
done

[ "$KEEP_IMAGES" = 1 ] || docker image rm kolo-release-nsis:arm64 $(printf "kolo-release-linux:%s-$GO_VERSION " $LINUX_ARCHES) >/dev/null
cd "$out"
shasum -a 256 -- "$name"-* > SHA256SUMS
ls -l "$out"
