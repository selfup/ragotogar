#!/usr/bin/env bash
set -euo pipefail

if command -v conda >/dev/null 2>&1; then
    conda config --add channels conda-forge
    conda install -y conda-forge::imagemagick conda-forge::exiftool
else
    echo "conda not found — skipping imagemagick/exiftool install"
fi

mkdir -p test_images

for i in {1..10}
do
    curl -L -o test_images/test${i}.jpg https://picsum.photos/1920/1080
done
