package main

import (
	"bytes"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
)

func main() {
	workers := flag.Int("workers", 8, "parallel workers")
	flag.Parse()

	dir := "."
	if flag.NArg() > 0 {
		dir = flag.Arg(0)
	}

	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	photoMjs := filepath.Join(cwd, "cashier", "photo.mjs")

	var files []string
	err = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if strings.HasSuffix(path, ".json") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error walking %s: %v\n", dir, err)
		os.Exit(1)
	}

	if len(files) == 0 {
		fmt.Printf("no .json files found in %s\n", dir)
		return
	}

	fmt.Printf("found %d .json file(s) in %s (workers: %d)\n\n", len(files), dir, *workers)

	sem := make(chan struct{}, *workers)
	var wg sync.WaitGroup
	var processed, errors atomic.Int64

	for _, f := range files {
		wg.Add(1)
		sem <- struct{}{}
		go func(jsonPath string) {
			defer wg.Done()
			defer func() { <-sem }()

			mdPath := strings.TrimSuffix(jsonPath, ".json") + ".md"
			var stderr bytes.Buffer
			cmd := exec.Command("node", photoMjs, jsonPath, mdPath)
			cmd.Stderr = &stderr
			if err := cmd.Run(); err != nil {
				fmt.Fprintf(os.Stderr, "  [error] %s:\n%s\n", jsonPath, strings.TrimSpace(stderr.String()))
				errors.Add(1)
				return
			}
			fmt.Printf("  [ok] %s\n", mdPath)
			processed.Add(1)
		}(f)
	}

	wg.Wait()
	fmt.Printf("\ndone. processed: %d, errors: %d\n", processed.Load(), errors.Load())
}
