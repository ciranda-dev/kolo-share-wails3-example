#!/usr/bin/env bash
# Builds every platform icon of one Kolo app from its mark sources.
# Usage: make-icons.sh [<src-dir> <build-dir> <app-name>]
#   <src-dir>   holds mark-h0.svg, mark-h1.svg, mark-h2.svg and scalable.svg
#   <build-dir> is the Wails build/ folder to fill
#   <app-name>  names the Linux icons, for example kolo-chat
# With no arguments, inside an app repository: reads build/icon-src and its app-name file.
# Needs rsvg-convert, ImageMagick (magick) and, on macOS, iconutil.
set -euo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
if [ $# -eq 0 ] && [ -f "$HERE/icon-src/app-name" ]; then set -- "$HERE/icon-src" "$HERE" "$(cat "$HERE/icon-src/app-name")"; fi
[ $# -eq 3 ] || { echo "usage: $0 [<src-dir> <build-dir> <app-name>]" >&2; exit 2; }
SRC="$(cd "$1" && pwd)"; mkdir -p "$2"; OUT="$(cd "$2" && pwd)"; NAME="$3"
BG='#070b12'
W="$(mktemp -d)"; trap 'rm -rf "${W:?}"' EXIT
mkdir -p "$OUT/darwin" "$OUT/windows"

# mark <px> <weight 0|1|2> <out>; weight 2 is the small mark, drawn to fill the whole tile
mark() { rsvg-convert -w "$1" -h "$1" "$SRC/mark-h$2.svg" -o "$3"; }
# tile <size> <radius> <mark px> <weight> <out>: rounded dark square with the mark centered
tile() {
  magick -size "$1x$1" xc:none -fill "$BG" -draw "roundrectangle 0,0 $(($1-1)),$(($1-1)) $2,$2" "$W/t.png"
  mark "$3" "$4" "$W/m.png"
  magick "$W/t.png" "$W/m.png" -gravity center -composite -depth 8 "PNG32:$5"
}

# macOS: 1024 canvas, 824 body (100 px margin), radius 185, mark 60% of the body, soft shadow below
magick -size 1024x1024 xc:none -fill "$BG" -draw "roundrectangle 100,100 923,923 185,185" "$W/body.png"
magick "$W/body.png" \( +clone -background black -shadow 30x14+0+12 \) +swap -background none -layers merge +repage -gravity center -extent 1024x1024 "$W/body-shadow.png"
mark 494 0 "$W/m.png"
magick "$W/body-shadow.png" "$W/m.png" -gravity center -composite -depth 8 "PNG32:$OUT/appicon.png"
if command -v iconutil >/dev/null; then
  S="$W/icons.iconset"; mkdir "$S"
  # the two smallest slots use a full tile with a heavy mark, the rest scale the 1024 tile
  tile 16 3 16 2 "$S/icon_16x16.png"
  tile 32 7 26 1 "$S/icon_16x16@2x.png"; cp "$S/icon_16x16@2x.png" "$S/icon_32x32.png"
  for p in 64:icon_32x32@2x 128:icon_128x128 256:icon_128x128@2x 256:icon_256x256 512:icon_256x256@2x 512:icon_512x512 1024:icon_512x512@2x; do
    magick "$OUT/appicon.png" -filter Lanczos -resize "${p%%:*}x${p%%:*}" "$S/${p##*:}.png"
  done
  iconutil -c icns "$S" -o "$OUT/darwin/icons.icns"
  cp "$OUT/darwin/icons.icns" "$OUT/darwin/dmg-file-icon.icns"; cp "$OUT/appicon.png" "$OUT/darwin/dmg-file-icon.png"
fi

# Windows: full rounded tile (radius 20%), heavier marks up to 48, one .ico with 7 sizes
mkdir -p "$W/win"
tile 16 3 16 2 "$W/win/16.png"
tile 24 5 24 2 "$W/win/24.png"
tile 32 6 25 1 "$W/win/32.png"
tile 48 10 38 1 "$W/win/48.png"
tile 64 13 50 0 "$W/win/64.png"
tile 128 26 100 0 "$W/win/128.png"
tile 256 51 200 0 "$W/win/256.png"
magick "$W"/win/16.png "$W"/win/24.png "$W"/win/32.png "$W"/win/48.png "$W"/win/64.png "$W"/win/128.png "$W"/win/256.png "$OUT/windows/icon.ico"

# Linux: freedesktop hicolor sizes, rounded body with a 4% margin (none up to 24), plus a scalable SVG
for s in 16 22 24 32 48 64 128 256 512; do
  d="$OUT/linux/icons/hicolor/${s}x${s}/apps"; mkdir -p "$d"
  m=$((s*4/100)); [ $s -le 24 ] && m=0
  body=$((s-2*m)); rad=$((body*225/1000)); mk=$((body*60/100)); h=0
  if [ $s -le 24 ]; then h=2; mk=$body; elif [ $s -le 48 ]; then h=1; mk=$((body*78/100)); fi
  magick -size "${s}x${s}" xc:none -fill "$BG" -draw "roundrectangle $m,$m $((s-m-1)),$((s-m-1)) $rad,$rad" "$W/t.png"
  mark "$mk" "$h" "$W/m.png"
  magick "$W/t.png" "$W/m.png" -gravity center -composite -depth 8 "PNG32:$d/$NAME.png"
done
mkdir -p "$OUT/linux/icons/hicolor/scalable/apps"
cp "$SRC/scalable.svg" "$OUT/linux/icons/hicolor/scalable/apps/$NAME.svg"
