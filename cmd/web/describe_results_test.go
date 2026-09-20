//go:build unix

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ragotogar/library"
)

func TestDescribeResultsFollowPartialWritesAndKeepIndexSelection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "results.jsonl")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	record := library.DescribeResult{Name: "one", Status: "described", Description: json.RawMessage(`{"subject":"a <tree>"}`)}
	if err := enc.Encode(record); err != nil {
		t.Fatal(err)
	}
	record.Status, record.IndexReady = "classified", true
	record.Classification = json.RawMessage(`{"subject_category":["nature"]}`)
	encoded, _ := json.Marshal(record)
	cut := len(encoded) / 2
	f.Write(encoded[:cut])
	r := describeResults{path: path}
	if err := r.refresh(false); err != nil || len(r.photos) != 1 || r.ready["one"] {
		t.Fatalf("partial record consumed: %+v %v", r, err)
	}
	s := &describeServer{latest: &describeRun{view: describeRunView{ID: "one"}, results: r}}
	w := httptest.NewRecorder()
	s.results(w, httptest.NewRequest("GET", "/describe/results?id=one", nil))
	dec := json.NewDecoder(w.Body)
	var downloaded library.DescribeResult
	if err := dec.Decode(&downloaded); err != nil || downloaded.Status != "described" || w.Code != 200 {
		t.Fatalf("download omitted complete record: %+v %v", downloaded, err)
	}
	if err := dec.Decode(&downloaded); err != io.EOF {
		t.Fatalf("download included an incomplete record: %v", err)
	}
	f.Write(append(encoded[cut:], '\n'))
	if err := r.refresh(false); err != nil || len(r.photos) != 1 || !r.ready["one"] || !strings.Contains(r.photos[0].Classification, "nature") {
		t.Fatalf("completed record lost: %+v %v", r, err)
	}
	for i := range describeResultLimit + 5 {
		record.Name = fmt.Sprintf("photo-%d", i)
		if err := enc.Encode(record); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.refresh(true); err != nil || len(r.photos) != describeResultLimit || len(r.ready) != describeResultLimit+6 || !r.ready["one"] {
		t.Fatalf("bounded display lost index targets: displayed=%d targets=%d err=%v", len(r.photos), len(r.ready), err)
	}
	if err := r.refresh(true); err != nil || len(r.ready) != describeResultLimit+6 {
		t.Fatalf("repeated poll changed results: %v", err)
	}
}

func TestDescribeIndexIncludesClassificationAndHidesInternalPaths(t *testing.T) {
	p := parseDescribeParams(url.Values{"index": {"1"}, "dir": {"/photos"}})
	if !p.classify || !p.index {
		t.Fatal("index must enable classification even without JavaScript")
	}
	p.resultsJSONL = "/temporary/private.jsonl"
	cmd := buildDescribeCommand(p)
	if !strings.Contains(cmd, "-classify") || strings.Contains(cmd, "private.jsonl") {
		t.Fatalf("incorrect displayed command: %s", cmd)
	}
}

func TestDescribeIndexFailureAndCancellation(t *testing.T) {
	for _, mode := range []string{"failure", "parent"} {
		t.Run(mode, func(t *testing.T) {
			s := newTestDescriber(t, "success")
			describeCmd := s.command
			s.command = func(ctx context.Context, p describeParams) *exec.Cmd {
				f, err := os.Create(p.resultsJSONL)
				if err != nil {
					t.Fatal(err)
				}
				enc := json.NewEncoder(f)
				for _, record := range []library.DescribeResult{
					{Name: "selected", Status: "classified", IndexReady: true},
					{Name: "failed", Status: "classification failed"},
					{Name: "existing", Status: "skipped"},
				} {
					if err := enc.Encode(record); err != nil {
						t.Error(err)
					}
				}
				f.Close()
				return describeCmd(ctx, p)
			}
			selected := make(chan string, 1)
			s.indexCommand = func(ctx context.Context, file string) *exec.Cmd {
				data, err := os.ReadFile(file)
				if err != nil {
					t.Error(err)
				}
				selected <- string(data)
				cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestDescribeRunHelper$")
				cmd.Env = append(os.Environ(), "RAGOTOGAR_DESCRIBE_TEST_HELPER="+mode)
				return cmd
			}
			if _, err := s.start(parseDescribeParams(url.Values{"dir": {s.repo}, "index": {"1"}})); err != nil {
				t.Fatal(err)
			}
			if mode == "parent" {
				v := waitDescribe(t, s, 5*time.Second, func(v *describeRunView) bool { return v.Phase == "index" && strings.Contains(v.Output, "listening=") })
				s.cancelRun(v.ID)
			}
			v := waitDescribe(t, s, 5*time.Second, func(v *describeRunView) bool { return !v.Active })
			wantStatus := "failed"
			if mode == "parent" {
				wantStatus = "canceled"
			}
			if v.Status != wantStatus || v.Phase != "index" || len(v.Photos) != 3 {
				t.Fatalf("index outcome lost report: %+v", v)
			}
			if got := <-selected; got != `["selected"]` {
				t.Fatalf("indexed failed/skipped photo: %s", got)
			}
		})
	}
}
