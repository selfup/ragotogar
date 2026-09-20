package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"

	"ragotogar/library"
)

const describeResultLimit = 20

type describePhotoView struct {
	Name           string `json:"name"`
	Path           string `json:"path"`
	Status         string `json:"status"`
	Description    string `json:"description"`
	Classification string `json:"classification"`
	Error          string `json:"error,omitempty"`
}

// Incrementally consume complete JSON records; an in-flight trailing record is
// retried on the next poll. Only twenty full snapshots stay in memory. The
// report on disk retains every record; its explicit IndexReady field drives
// the next stage, independently of the bounded UI/log tails.
type describeResults struct {
	path   string
	offset int64
	photos []describePhotoView
	ready  map[string]bool
}

func prettyJSON(raw json.RawMessage) string {
	var b bytes.Buffer
	if len(raw) == 0 || json.Indent(&b, raw, "", "  ") != nil {
		return "null"
	}
	return b.String()
}

// Caller holds describeServer.mu.
func (r *describeResults) refresh(final bool) error {
	f, err := os.Open(r.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil // dry runs and runs still preparing have no records yet
	}
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Seek(r.offset, io.SeekStart); err != nil {
		return err
	}
	base := r.offset
	dec := json.NewDecoder(f)
	for {
		var record library.DescribeResult
		if err := dec.Decode(&record); err != nil {
			if errors.Is(err, io.EOF) || (!final && errors.Is(err, io.ErrUnexpectedEOF)) {
				return nil
			}
			return fmt.Errorf("read photo results: %w", err)
		}
		r.offset = base + dec.InputOffset()
		if r.ready == nil {
			r.ready = make(map[string]bool)
		}
		r.ready[record.Name] = record.IndexReady
		view := describePhotoView{record.Name, record.Path, record.Status,
			prettyJSON(record.Description), prettyJSON(record.Classification), record.Error}
		if i := slices.IndexFunc(r.photos, func(v describePhotoView) bool { return v.Name == record.Name }); i >= 0 {
			r.photos = slices.Delete(r.photos, i, i+1)
		}
		r.photos = append(r.photos, view)
		if len(r.photos) > describeResultLimit {
			r.photos = slices.Delete(r.photos, 0, len(r.photos)-describeResultLimit)
		}
	}
}

func (s *describeServer) results(w http.ResponseWriter, req *http.Request) {
	s.mu.Lock()
	j := s.latest
	if j == nil || j.view.ID != req.URL.Query().Get("id") {
		s.mu.Unlock()
		http.NotFound(w, req)
		return
	}
	if err := j.results.refresh(false); err != nil {
		s.mu.Unlock()
		http.Error(w, "cannot read saved photo results", http.StatusInternalServerError)
		return
	}
	size := j.results.offset
	f, err := os.Open(j.results.path)
	s.mu.Unlock()
	if err != nil {
		http.Error(w, "no saved photo results yet", http.StatusNotFound)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Content-Disposition", `attachment; filename="describe-results.jsonl"`)
	// A live run may be writing the next record. Download only complete records
	// and include the final newline that json.Decoder.InputOffset omits.
	if _, err := io.Copy(w, io.LimitReader(f, size)); err == nil && size > 0 {
		_, _ = io.WriteString(w, "\n")
	}
}
