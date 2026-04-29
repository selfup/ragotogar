package main

import (
	"database/sql"
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

// search shells out to scripts/search.sh (cmd/search) and returns results
// that exist in the SQL library. Order is preserved from the search output
// (vector retrieval order, highest similarity first).
//
// Mode "naive-verify" composes -retrieve -verify so an LLM yes/no check
// runs on each candidate; only YES matches survive. Other modes map to
// plain -retrieve since pgvector doesn't have graph/hybrid concepts.
func search(db *sql.DB, query, mode, repoRoot string) []result {
	args := []string{"-retrieve"}
	if mode == "naive-verify" {
		args = append(args, "-verify")
	}
	args = append(args, query)

	cmd := exec.Command("./scripts/search.sh", args...)
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
		if !photoExists(db, name) {
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
