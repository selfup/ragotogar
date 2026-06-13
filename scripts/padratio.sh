#!/usr/bin/env bash
# padratio.sh — pad an image to a target aspect ratio (no crop)
#
# Scope: raster images only (JPEG / PNG / TIFF). Not for RAW (.raf/.arw/.nef/…)
# — those would be re-encoded into a meaningless same-extension file. Extract a
# JPEG/TIFF first if you need to pad a RAW frame.
#
# What it does: computes the smallest canvas of the target ratio that fully
# contains the source (never crops), then centers the image on a solid-color
# background. Orientation follows the image — landscape stays landscape.
#
# Usage:
#   ./padratio.sh <ratio> <input> [output] [color]
#
#   ratio   3:2 | 5:4   (orientation follows the image: landscape stays landscape)
#   input   source image
#   output  optional; defaults to <input>_<ratio>.<ext>
#   color   optional border color; defaults to white
#
# Examples:
#   ./padratio.sh 3:2 shot.jpg
#   ./padratio.sh 5:4 shot.jpg framed.jpg black
#
# Requires ImageMagick (v6 `convert` or v7 `magick`).

set -euo pipefail

RATIO="${1:?ratio required: 3:2 or 5:4}"
INPUT="${2:?input image required}"
COLOR="${4:-white}"

[[ -f "$INPUT" ]] || { echo "error: '$INPUT' not found" >&2; exit 1; }

# pick magick (v7) or convert (v6)
if command -v magick >/dev/null 2>&1; then IM=(magick)
elif command -v convert >/dev/null 2>&1; then IM=(convert)
else echo "error: ImageMagick not found (need 'magick' or 'convert')" >&2; exit 1; fi

case "$RATIO" in
  3:2) RW=3; RH=2 ;;
  5:4) RW=5; RH=4 ;;
  *) echo "error: ratio must be 3:2 or 5:4" >&2; exit 1 ;;
esac

# default output name
EXT="${INPUT##*.}"
BASE="${INPUT%.*}"
SAFE_RATIO="${RATIO/:/x}"
OUTPUT="${3:-${BASE}_${SAFE_RATIO}.${EXT}}"

# read source dimensions
W=$("${IM[@]}" identify -format '%w' "$INPUT")
H=$("${IM[@]}" identify -format '%h' "$INPUT")

# match target orientation to the image: ratio long side = image long side
if (( W >= H )); then long=$RW; short=$RH; else long=$RH; short=$RW; fi

# compute the smallest canvas of that ratio that fully contains WxH (no crop)
# need CW/CH == long/short, CW>=W, CH>=H
# candidate A: lock width  -> CW=W,  CH=ceil(W*short/long)
# candidate B: lock height -> CH=H,  CW=ceil(H*long/short)
chA=$(( (W*short + long-1) / long ))   # canvas height if width is locked
cwB=$(( (H*long + short-1) / short ))  # canvas width if height is locked

if (( chA >= H )); then CW=$W;   CH=$chA
else                    CW=$cwB; CH=$H
fi

echo "source ${W}x${H} -> ${RATIO} canvas ${CW}x${CH}  color=${COLOR}  out=${OUTPUT}"

"${IM[@]}" "$INPUT" \
  -background "$COLOR" -gravity center -extent "${CW}x${CH}" \
  "$OUTPUT"
