package main

import (
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
	workers := flag.Int("workers", 8, "parallel render workers")
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
	cliPath := filepath.Join(cwd, "cashier", "cli.mjs")

	var files []string
	err = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if strings.HasSuffix(path, ".md") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error walking %s: %v\n", dir, err)
		os.Exit(1)
	}

	if len(files) == 0 {
		fmt.Printf("no .md files found in %s\n", dir)
		return
	}

	fmt.Printf("found %d .md file(s) in %s (workers: %d)\n\n", len(files), dir, *workers)

	sem := make(chan struct{}, *workers)
	var wg sync.WaitGroup
	var processed, errors atomic.Int64

	for _, f := range files {
		wg.Add(1)
		sem <- struct{}{}
		go func(mdPath string) {
			defer wg.Done()
			defer func() { <-sem }()

			htmlPath := strings.TrimSuffix(mdPath, ".md") + ".html"
			cmd := exec.Command("node", cliPath, "build", mdPath)
			out, err := cmd.Output()
			if err != nil {
				if exitErr, ok := err.(*exec.ExitError); ok {
					fmt.Fprintf(os.Stderr, "  [error] %s:\n%s\n", mdPath, strings.TrimSpace(string(exitErr.Stderr)))
				} else {
					fmt.Fprintf(os.Stderr, "  [error] %s: %v\n", mdPath, err)
				}
				errors.Add(1)
				return
			}
			if err := os.WriteFile(htmlPath, out, 0644); err != nil {
				fmt.Fprintf(os.Stderr, "  [error] write %s: %v\n", htmlPath, err)
				errors.Add(1)
				return
			}
			fmt.Printf("  [ok] %s\n", htmlPath)
			processed.Add(1)
		}(f)
	}

	wg.Wait()
	fmt.Printf("\ndone. processed: %d, errors: %d\n", processed.Load(), errors.Load())
}
