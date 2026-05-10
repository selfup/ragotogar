package main

import (
	"bufio"
	"database/sql"
	"encoding/binary"
	"fmt"
	"os"

	"github.com/blevesearch/vellum"
)

// fstStats reports the build outputs for the lexical lane.
type fstStats struct {
	UniqueTerms   int
	TotalPostings int
	FSTBytes      int64
	PostingsBytes int64
}

// fstWriter is the testable core of the lexical-lane build. Drive it
// with sorted (lexeme, compact_id) pairs via Add; Close flushes the
// final group, finalizes the FST, and reports stats. Vellum requires
// lexicographic key insertion order, so callers MUST sort by (lexeme,
// compact_id) before calling Add — Add returns the vellum error
// directly on out-of-order keys.
//
// Within one lexeme group, postings are delta-encoded; duplicate (lex,
// compact_id) pairs are collapsed (set semantics, not multiset).
type fstWriter struct {
	postFile *os.File
	postBW   *bufio.Writer
	fstFile  *os.File
	fstBW    *bufio.Writer
	builder  *vellum.Builder

	scratch []byte

	// Per-group state
	prevLex     string
	prevID      uint32
	havePrev    bool
	groupBuf    []byte
	groupCount  int
	groupOffset int64
	offset      int64

	stats fstStats
}

func newFSTWriter(fstPath, postingsPath string) (*fstWriter, error) {
	postFile, err := os.Create(postingsPath)
	if err != nil {
		return nil, err
	}
	fstFile, err := os.Create(fstPath)
	if err != nil {
		postFile.Close()
		return nil, err
	}
	w := &fstWriter{
		postFile: postFile,
		postBW:   bufio.NewWriter(postFile),
		fstFile:  fstFile,
		fstBW:    bufio.NewWriter(fstFile),
		scratch:  make([]byte, binary.MaxVarintLen64),
	}
	w.builder, err = vellum.New(w.fstBW, nil)
	if err != nil {
		postFile.Close()
		fstFile.Close()
		return nil, fmt.Errorf("vellum.New: %w", err)
	}
	return w, nil
}

// Add appends a (lexeme, compact_id) pair. Pairs MUST arrive in
// (lexeme ASC, compact_id ASC) order. Duplicate (lexeme, compact_id)
// pairs are silently collapsed.
//
// Order is validated eagerly at every Add — out-of-order lexeme or
// compact_id surfaces here rather than at Close. The deferred path
// would let the writer stream thousands of postings to disk before
// failing, and an out-of-order compact_id would silently underflow
// the delta computation (uint32 wrap) producing a corrupt posting
// list rather than an error.
func (w *fstWriter) Add(lexeme string, cid uint32) error {
	if w.havePrev {
		if lexeme < w.prevLex {
			return fmt.Errorf("fstWriter: out-of-order lexeme %q after %q", lexeme, w.prevLex)
		}
		if lexeme == w.prevLex && w.groupCount > 0 && cid < w.prevID {
			return fmt.Errorf("fstWriter: out-of-order compact_id %d after %d for lexeme %q", cid, w.prevID, lexeme)
		}
	}
	if lexeme != w.prevLex || !w.havePrev {
		if err := w.flushGroup(); err != nil {
			return err
		}
		w.prevLex = lexeme
		w.prevID = 0
		w.havePrev = true
		w.groupBuf = w.groupBuf[:0]
		w.groupCount = 0
		w.groupOffset = w.offset
	} else if w.groupCount > 0 && cid == w.prevID {
		// Same lexeme, same compact_id — duplicate. Skip.
		return nil
	}
	delta := cid - w.prevID
	n := binary.PutUvarint(w.scratch, uint64(delta))
	w.groupBuf = append(w.groupBuf, w.scratch[:n]...)
	w.groupCount++
	w.prevID = cid
	return nil
}

func (w *fstWriter) flushGroup() error {
	if !w.havePrev {
		return nil
	}
	n := binary.PutUvarint(w.scratch, uint64(w.groupCount))
	if _, err := w.postBW.Write(w.scratch[:n]); err != nil {
		return err
	}
	w.offset += int64(n)
	if _, err := w.postBW.Write(w.groupBuf); err != nil {
		return err
	}
	w.offset += int64(len(w.groupBuf))

	if err := w.builder.Insert([]byte(w.prevLex), uint64(w.groupOffset)); err != nil {
		return fmt.Errorf("vellum.Insert(%q): %w", w.prevLex, err)
	}
	w.stats.UniqueTerms++
	w.stats.TotalPostings += w.groupCount
	return nil
}

// Close flushes the final group, finalizes the FST, and closes both
// files. Always returns the stats — useful for diagnostics even on
// error.
func (w *fstWriter) Close() (fstStats, error) {
	if err := w.flushGroup(); err != nil {
		w.postFile.Close()
		w.fstFile.Close()
		return w.stats, err
	}
	if err := w.builder.Close(); err != nil {
		w.postFile.Close()
		w.fstFile.Close()
		return w.stats, fmt.Errorf("vellum.Close: %w", err)
	}
	if err := w.fstBW.Flush(); err != nil {
		return w.stats, err
	}
	if err := w.postBW.Flush(); err != nil {
		return w.stats, err
	}
	if fi, err := w.fstFile.Stat(); err == nil {
		w.stats.FSTBytes = fi.Size()
	}
	if fi, err := w.postFile.Stat(); err == nil {
		w.stats.PostingsBytes = fi.Size()
	}
	if err := w.fstFile.Close(); err != nil {
		w.postFile.Close()
		return w.stats, err
	}
	if err := w.postFile.Close(); err != nil {
		return w.stats, err
	}
	return w.stats, nil
}

// buildFSTAndPostings reads (lexeme, photo_id) pairs from
// descriptions.fts ‖ exif.fts, sorts via SQL (ORDER BY lexeme,
// photo_id), maps photo_id → compact_id, and feeds them to fstWriter.
// The SQL sort satisfies vellum's lexicographic insertion requirement;
// the within-group compact-id ordering matches id_space ordering
// because id_space is itself ORDER BY name and photo_id == name.
func buildFSTAndPostings(db *sql.DB, ids *idSpace, fstPath, postingsPath string) (fstStats, error) {
	w, err := newFSTWriter(fstPath, postingsPath)
	if err != nil {
		return fstStats{}, err
	}

	// COLLATE "C" forces byte-wise lexicographic ordering. Without it,
	// pg's default locale collation (often en_US-ish on developer
	// machines) reorders punctuation-leading lexemes by language rules
	// — producing pairs like "+0.33" *after* "/wooden" because the
	// locale sorts `/` before `+`, which trips fstWriter's eager order
	// check (vellum requires byte-wise insert order). Both columns
	// are TEXT so both need the explicit collation.
	rows, err := db.Query(`
		SELECT lexeme, photo_id FROM (
		  SELECT photo_id, unnest(tsvector_to_array(fts)) AS lexeme FROM descriptions
		  UNION ALL
		  SELECT photo_id, unnest(tsvector_to_array(fts)) AS lexeme FROM exif
		) t
		ORDER BY lexeme COLLATE "C", photo_id COLLATE "C"
	`)
	if err != nil {
		w.Close()
		return fstStats{}, fmt.Errorf("query lexemes: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var lex, name string
		if err := rows.Scan(&lex, &name); err != nil {
			w.Close()
			return fstStats{}, err
		}
		cid, ok := ids.CompactID(name)
		if !ok {
			// References a photo not in id_space — shouldn't happen
			// post-FK, but skip rather than corrupt the lane.
			continue
		}
		if err := w.Add(lex, cid); err != nil {
			w.Close()
			return fstStats{}, err
		}
	}
	if err := rows.Err(); err != nil {
		w.Close()
		return fstStats{}, err
	}
	return w.Close()
}
