package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// rawExts mirrors cmd/describe — extensions where ImageMagick can't decode
// natively and we should pull the embedded JPEG via exiftool first.
var rawExts = map[string]bool{
	"raf": true, "arw": true, "nef": true,
	"cr2": true, "cr3": true, "dng": true,
	"orf": true, "rw2": true, "pef": true,
}

// resolveImageSource returns a path ImageMagick can read directly. For RAW
// formats it extracts the embedded preview JPEG via exiftool to a temp file;
// callers must invoke cleanup() when done to remove that temp.
func resolveImageSource(src string) (path string, cleanup func(), err error) {
	src = strings.TrimPrefix(src, "file://")
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(src), "."))
	if !rawExts[ext] {
		return src, func() {}, nil
	}
	tmp, err := os.CreateTemp("", "cashier_raw_*.jpg")
	if err != nil {
		return "", nil, err
	}
	tmpPath := tmp.Name()
	tmp.Close()
	out, err := exec.Command("exiftool", "-b", "-PreviewImage", src).Output()
	if err != nil || len(out) == 0 {
		os.Remove(tmpPath)
		return "", nil, fmt.Errorf("no embedded preview in %s (need exiftool, or non-RAW source)", src)
	}
	if err := os.WriteFile(tmpPath, out, 0644); err != nil {
		os.Remove(tmpPath)
		return "", nil, err
	}
	return tmpPath, func() { os.Remove(tmpPath) }, nil
}

// writeThumbnail uses ImageMagick to write a resized JPEG of srcPath to dstPath.
// The "Nx>" geometry only shrinks larger sources; smaller images pass through.
// RAW formats are routed through resolveImageSource → embedded JPEG first.
func writeThumbnail(srcPath, dstPath string, width int) error {
	path, cleanup, err := resolveImageSource(srcPath)
	if err != nil {
		return err
	}
	defer cleanup()
	geometry := fmt.Sprintf("%dx>", width)
	cmd := exec.Command("magick", path, "-resize", geometry, "-quality", "85", dstPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("magick %s -> %s: %s: %w", path, dstPath, out, err)
	}
	return nil
}
