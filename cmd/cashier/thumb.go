package main

import (
	"fmt"
	"os/exec"
	"strings"
)

// writeThumbnail uses ImageMagick to write a resized JPEG of srcPath to dstPath.
// The "Nx>" geometry only shrinks larger sources; smaller images pass through.
func writeThumbnail(srcPath, dstPath string, width int) error {
	src := strings.TrimPrefix(srcPath, "file://")
	geometry := fmt.Sprintf("%dx>", width)
	cmd := exec.Command("magick", src, "-resize", geometry, "-quality", "85", dstPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("magick %s -> %s: %s: %w", src, dstPath, out, err)
	}
	return nil
}
