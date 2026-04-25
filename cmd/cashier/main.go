package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}
	cmd := os.Args[1]
	args := os.Args[2:]

	switch cmd {
	case "photo":
		runPhoto(args)
	case "build":
		runBuild(args)
	case "photo-all":
		runBatch(args, ".json", runPhotoFile, "photo-all")
	case "build-all":
		runBatch(args, ".md", runBuildFile, "build-all")
	case "all":
		runAll(args)
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", cmd)
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Fprintln(os.Stderr, `cashier — photo pipeline (Go port)

usage:
  cashier photo    <input.json> <output.md>
  cashier build    <input.md>  <output.html>
  cashier photo-all [-workers N] <dir>
  cashier build-all [-workers N] <dir>
  cashier all       [-workers N] <dir>`)
}

// ── styles.css loader ─────────────────────────────────────────────────────

func loadStyles() string {
	dir, _ := os.Getwd()
	for {
		candidate := filepath.Join(dir, "styles.css")
		if data, err := os.ReadFile(candidate); err == nil {
			return string(data)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return ""
}

// ── single-file commands ──────────────────────────────────────────────────

func runPhoto(args []string) {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: cashier photo <input.json> <output.md>")
		os.Exit(1)
	}
	md, err := photoFromFile(args[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := os.WriteFile(args[1], []byte(md), 0644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Fprintln(os.Stderr, "wrote", args[1])
}

func runBuild(args []string) {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: cashier build <input.md> <output.html>")
		os.Exit(1)
	}
	html, err := buildFromFile(args[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := os.WriteFile(args[1], []byte(html), 0644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Fprintln(os.Stderr, "wrote", args[1])
}

// ── file helpers ──────────────────────────────────────────────────────────

func photoFromFile(jsonPath string) (string, error) {
	raw, err := os.ReadFile(jsonPath)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", jsonPath, err)
	}
	var data PhotoData
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&data); err != nil {
		return "", fmt.Errorf("decode %s: %w", jsonPath, err)
	}
	return buildMarkdown(data), nil
}

func buildFromFile(mdPath string) (string, error) {
	raw, err := os.ReadFile(mdPath)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", mdPath, err)
	}
	return buildHTML(string(raw), loadStyles())
}

func runPhotoFile(jsonPath string) error {
	md, err := photoFromFile(jsonPath)
	if err != nil {
		return err
	}
	outPath := strings.TrimSuffix(jsonPath, ".json") + ".md"
	return os.WriteFile(outPath, []byte(md), 0644)
}

func runBuildFile(mdPath string) error {
	html, err := buildFromFile(mdPath)
	if err != nil {
		return err
	}
	outPath := strings.TrimSuffix(mdPath, ".md") + ".html"
	return os.WriteFile(outPath, []byte(html), 0644)
}

// ── batch worker pool ─────────────────────────────────────────────────────

func runBatch(args []string, ext string, process func(string) error, cmdName string) {
	fs2 := flag.NewFlagSet(cmdName, flag.ExitOnError)
	workers := fs2.Int("workers", 8, "parallel workers")
	fs2.Parse(args)

	dir := "."
	if fs2.NArg() > 0 {
		dir = fs2.Arg(0)
	}

	var files []string
	filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if strings.HasSuffix(path, ext) {
			files = append(files, path)
		}
		return nil
	})

	if len(files) == 0 {
		fmt.Printf("no %s files found in %s\n", ext, dir)
		return
	}
	fmt.Printf("found %d %s file(s) in %s (workers: %d)\n\n", len(files), ext, dir, *workers)

	sem := make(chan struct{}, *workers)
	var wg sync.WaitGroup
	var processed, errors atomic.Int64

	for _, f := range files {
		wg.Add(1)
		sem <- struct{}{}
		go func(path string) {
			defer wg.Done()
			defer func() { <-sem }()
			if err := process(path); err != nil {
				fmt.Fprintf(os.Stderr, "  [error] %s: %v\n", path, err)
				errors.Add(1)
				return
			}
			out := strings.TrimSuffix(path, ext)
			if ext == ".json" {
				out += ".md"
			} else {
				out += ".html"
			}
			fmt.Printf("  [ok] %s\n", out)
			processed.Add(1)
		}(f)
	}
	wg.Wait()
	fmt.Printf("\ndone. processed: %d, errors: %d\n", processed.Load(), errors.Load())
}

func runAll(args []string) {
	fs2 := flag.NewFlagSet("all", flag.ExitOnError)
	workers := fs2.Int("workers", 8, "parallel workers")
	fs2.Parse(args)

	dir := "."
	if fs2.NArg() > 0 {
		dir = fs2.Arg(0)
	}

	var files []string
	filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if strings.HasSuffix(path, ".json") {
			files = append(files, path)
		}
		return nil
	})

	if len(files) == 0 {
		fmt.Printf("no .json files found in %s\n", dir)
		return
	}
	fmt.Printf("found %d .json file(s) in %s (workers: %d)\n\n", len(files), dir, *workers)

	sem := make(chan struct{}, *workers)
	var wg sync.WaitGroup
	var processed, errors atomic.Int64
	styles := loadStyles()

	for _, f := range files {
		wg.Add(1)
		sem <- struct{}{}
		go func(jsonPath string) {
			defer wg.Done()
			defer func() { <-sem }()

			stem := strings.TrimSuffix(jsonPath, ".json")
			md, err := photoFromFile(jsonPath)
			if err != nil {
				fmt.Fprintf(os.Stderr, "  [error] %s: %v\n", jsonPath, err)
				errors.Add(1)
				return
			}
			if err := os.WriteFile(stem+".md", []byte(md), 0644); err != nil {
				fmt.Fprintf(os.Stderr, "  [error] write md %s: %v\n", stem+".md", err)
				errors.Add(1)
				return
			}
			html, err := buildHTML(md, styles)
			if err != nil {
				fmt.Fprintf(os.Stderr, "  [error] build html %s: %v\n", stem+".md", err)
				errors.Add(1)
				return
			}
			if err := os.WriteFile(stem+".html", []byte(html), 0644); err != nil {
				fmt.Fprintf(os.Stderr, "  [error] write html %s: %v\n", stem+".html", err)
				errors.Add(1)
				return
			}
			fmt.Printf("  [ok] %s.html\n", stem)
			processed.Add(1)
		}(f)
	}
	wg.Wait()
	fmt.Printf("\ndone. processed: %d, errors: %d\n", processed.Load(), errors.Load())
}
