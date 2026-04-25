package main

import (
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// matches "  [N] <path>" lines printed by tools/search.py print_sources()
var searchLineRE = regexp.MustCompile(`^\s*\[(\d+)\]\s+(.+)$`)

// search shells out to tools/search.sh --retrieve --mode <mode> and returns
// results that have a .jpg sidecar in photoDir. Order is preserved from the
// search output (LightRAG retrieval order — most relevant first).
func search(query, mode, repoRoot, photoDir string) []result {
	cmd := exec.Command("./tools/search.sh", "--retrieve", "--mode", mode, query)
	cmd.Dir = repoRoot
	out, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("search %q (mode=%s): %v\n%s", query, mode, err, out)
		return nil
	}
	var results []result
	for _, name := range parseSearchOutput(string(out)) {
		if _, err := os.Stat(filepath.Join(photoDir, name+".jpg")); err != nil {
			continue
		}
		results = append(results, result{Name: name})
	}
	return results
}

// parseSearchOutput extracts unique photo basenames (without extension) from
// the stdout of tools/search.sh --retrieve, preserving retrieval order.
func parseSearchOutput(out string) []string {
	seen := make(map[string]bool)
	var names []string
	for _, line := range strings.Split(out, "\n") {
		m := searchLineRE.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		path := strings.TrimSpace(m[2])
		base := filepath.Base(path)
		name := strings.TrimSuffix(base, filepath.Ext(base))
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		names = append(names, name)
	}
	return names
}
