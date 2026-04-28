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

// search shells out to tools/search.sh and returns results that exist in
// the SQL library. Order is preserved from the search output (LightRAG
// retrieval order — most relevant first).
//
// Mode "naive-verify" is a compound: --retrieve --mode naive --verify, which
// runs an LLM yes/no check on each candidate and keeps only the YES matches.
// Verify pulls indexable text from SQL via tools/rag_common.fetch_photo_dict.
func search(db *sql.DB, query, mode, repoRoot string) []result {
	args := []string{"--retrieve"}
	if mode == "naive-verify" {
		args = append(args, "--mode", "naive", "--verify")
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
