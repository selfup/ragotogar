package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// matches "  [N] <path>" lines printed by tools/search.py print_sources()
var searchLineRE = regexp.MustCompile(`^\s*\[(\d+)\]\s+(.+)$`)

// search shells out to tools/search.sh and returns results that have a .jpg
// sidecar in photoDir. Order is preserved from the search output (LightRAG
// retrieval order — most relevant first).
//
// Mode "naive-verify" is a compound: --retrieve --mode naive --verify, which
// runs an LLM yes/no check on each candidate and keeps only the YES matches.
func search(query, mode, repoRoot, photoDir string) []result {
	args := []string{"--retrieve"}
	if mode == "naive-verify" {
		// --json-dir lets verify resolve LightRAG basenames back to readable JSONs
		args = append(args, "--mode", "naive", "--verify", "--json-dir", photoDir)
	} else {
		args = append(args, "--mode", mode)
	}
	args = append(args, query)

	cmd := exec.Command("./tools/search.sh", args...)
	cmd.Dir = repoRoot
	// Pass stderr through to the server's terminal so progress/debug output
	// from search.py (e.g. per-photo verify verdicts) is visible.
	cmd.Stderr = os.Stderr
	fmt.Fprintf(os.Stderr, "search: q=%q mode=%s\n", query, mode)
	out, err := cmd.Output()
	if err != nil {
		log.Printf("search %q (mode=%s): %v", query, mode, err)
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
	for line := range strings.SplitSeq(out, "\n") {
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
