package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ragotogar/library"
)

// TestRetrieveFromEdge_HappyPath verifies a 200 + well-formed JSON response
// translates into library.Result values with Name + Similarity populated.
func TestRetrieveFromEdge_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"hits":[{"name":"p_one","score":0.82},{"name":"p_two","score":0.41}]}`)
	}))
	defer srv.Close()

	results, err := retrieveFromEdge(context.Background(), srv.URL, "vector", "warm light",
		library.DefaultSearchOptionsV2())
	if err != nil {
		t.Fatalf("retrieveFromEdge: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("results len = %d, want 2", len(results))
	}
	if results[0].Name != "p_one" || results[0].Similarity != 0.82 {
		t.Errorf("results[0] = %+v", results[0])
	}
	if results[1].Name != "p_two" || results[1].Similarity != 0.41 {
		t.Errorf("results[1] = %+v", results[1])
	}
}

// TestRetrieveFromEdge_EmptyURLErrors verifies the guard at the top of the
// function. Callers expecting to silently pass through with no edge
// configured would otherwise emit a confusing parse error.
func TestRetrieveFromEdge_EmptyURLErrors(t *testing.T) {
	_, err := retrieveFromEdge(context.Background(), "", "vector", "q", library.DefaultSearchOptionsV2())
	if err == nil {
		t.Fatal("retrieveFromEdge with empty URL should error")
	}
	if !strings.Contains(err.Error(), "edge URL not configured") {
		t.Errorf("error message should mention configuration, got: %v", err)
	}
}

// TestRetrieveFromEdge_400SurfacesBodyVerbatim mirrors cmd/edge's behavior
// on phrase queries: HTTP 400 with a plain-text body. cmd/web's UI status
// line shows that body verbatim, so the dispatch path must surface it
// without rewriting.
func TestRetrieveFromEdge_400SurfacesBodyVerbatim(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "phrase queries (quoted) are not supported at the edge", http.StatusBadRequest)
	}))
	defer srv.Close()

	_, err := retrieveFromEdge(context.Background(), srv.URL, "vector", `"red truck"`, library.DefaultSearchOptionsV2())
	if err == nil {
		t.Fatal("expected error for 400 response")
	}
	if !strings.Contains(err.Error(), "phrase queries") {
		t.Errorf("error should contain edge's body, got: %v", err)
	}
	if !strings.Contains(err.Error(), "400") {
		t.Errorf("error should mention the status code, got: %v", err)
	}
}

// TestRetrieveFromEdge_500ErrorSurfaces covers transient backend errors —
// network failures show up as Go errors, but server-side errors come back
// as non-2xx + body. Both should produce non-nil err from the client.
func TestRetrieveFromEdge_500ErrorSurfaces(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal", http.StatusInternalServerError)
	}))
	defer srv.Close()

	_, err := retrieveFromEdge(context.Background(), srv.URL, "vector", "q", library.DefaultSearchOptionsV2())
	if err == nil {
		t.Fatal("expected error for 500 response")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error should mention 500, got: %v", err)
	}
}

func TestRetrieveFromEdge_MalformedJSONErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, `not json at all`)
	}))
	defer srv.Close()

	_, err := retrieveFromEdge(context.Background(), srv.URL, "vector", "q", library.DefaultSearchOptionsV2())
	if err == nil {
		t.Fatal("expected JSON decode error")
	}
	if !strings.Contains(err.Error(), "edge decode") {
		t.Errorf("error should name the decode step, got: %v", err)
	}
}

// TestRetrieveFromEdge_VectorModeDisablesLexical guards the mode→arm
// routing for vector-only modes. cmd/edge's `lexical=0` skips the FST arm
// entirely; getting this wrong inflates result counts with non-stemmed
// lexical matches.
func TestRetrieveFromEdge_VectorModeDisablesLexical(t *testing.T) {
	var capturedQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedQuery = r.URL.RawQuery
		fmt.Fprintln(w, `{"hits":[]}`)
	}))
	defer srv.Close()

	for _, mode := range []string{"vector", "vector-verify", "auto", "auto-verify", ""} {
		t.Run("mode="+mode, func(t *testing.T) {
			if _, err := retrieveFromEdge(context.Background(), srv.URL, mode, "q",
				library.DefaultSearchOptionsV2()); err != nil {
				t.Fatalf("retrieveFromEdge: %v", err)
			}
			if !strings.Contains(capturedQuery, "lexical=0") {
				t.Errorf("mode=%q should set lexical=0; raw query: %s", mode, capturedQuery)
			}
			if !strings.Contains(capturedQuery, "vector=1") {
				t.Errorf("mode=%q should set vector=1; raw query: %s", mode, capturedQuery)
			}
		})
	}
}

func TestRetrieveFromEdge_FTSVectorModeEnablesLexical(t *testing.T) {
	var capturedQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedQuery = r.URL.RawQuery
		fmt.Fprintln(w, `{"hits":[]}`)
	}))
	defer srv.Close()

	for _, mode := range []string{"fts-vector", "fts-vector-verify"} {
		t.Run("mode="+mode, func(t *testing.T) {
			if _, err := retrieveFromEdge(context.Background(), srv.URL, mode, "q",
				library.DefaultSearchOptionsV2()); err != nil {
				t.Fatalf("retrieveFromEdge: %v", err)
			}
			if !strings.Contains(capturedQuery, "lexical=1") {
				t.Errorf("mode=%q should set lexical=1; raw query: %s", mode, capturedQuery)
			}
		})
	}
}

// TestRetrieveFromEdge_AllParamsPropagated verifies the URL contract that
// cmd/edge depends on: every option from SearchOptionsV2 must reach the
// wire as the expected query parameter shape. Cosine threshold is optional
// (pointer), so test both with-threshold and without-threshold.
func TestRetrieveFromEdge_AllParamsPropagated(t *testing.T) {
	var capturedQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedQuery = r.URL.RawQuery
		fmt.Fprintln(w, `{"hits":[]}`)
	}))
	defer srv.Close()

	threshold := 0.42
	opts := library.SearchOptionsV2{
		VectorQuery:        "warm",
		UseDescriptions:    true,
		UseMetadata:        false,
		UseQueries:         true,
		MergeStrategy:      library.MergeWeighted,
		WeightDescriptions: 2.5,
		WeightMetadata:     0.0,
		WeightQueries:      1.25,
		Threshold:          &threshold,
	}

	if _, err := retrieveFromEdge(context.Background(), srv.URL, "fts-vector", "warm afternoon", opts); err != nil {
		t.Fatalf("retrieveFromEdge: %v", err)
	}
	// raw query is URL-encoded; check each param substring instead of full
	// match so the param order doesn't matter.
	wantSubstrings := []string{
		"q=warm+afternoon",
		"descriptions=1",
		"metadata=0",
		"queries=1",
		"merge=weighted",
		"wd=2.500000",
		"wm=0.000000",
		"wq=1.250000",
		"cosine=0.420000",
	}
	for _, sub := range wantSubstrings {
		if !strings.Contains(capturedQuery, sub) {
			t.Errorf("raw query missing %q; full: %s", sub, capturedQuery)
		}
	}
}

func TestRetrieveFromEdge_ThresholdNilOmitsCosineParam(t *testing.T) {
	var capturedQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedQuery = r.URL.RawQuery
		fmt.Fprintln(w, `{"hits":[]}`)
	}))
	defer srv.Close()

	opts := library.DefaultSearchOptionsV2() // Threshold is nil
	if _, err := retrieveFromEdge(context.Background(), srv.URL, "vector", "q", opts); err != nil {
		t.Fatalf("retrieveFromEdge: %v", err)
	}
	if strings.Contains(capturedQuery, "cosine=") {
		t.Errorf("nil Threshold should not emit cosine= param; raw: %s", capturedQuery)
	}
}

// TestRetrieveFromEdge_ContextCancellationSurfacesError verifies the
// http.Client respects ctx cancellation. Important because cmd/web's
// request handler ctx is what propagates here — a client timeout shouldn't
// hang the page render.
func TestRetrieveFromEdge_ContextCancellationSurfacesError(t *testing.T) {
	// Server that intentionally hangs forever.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := retrieveFromEdge(ctx, srv.URL, "vector", "q", library.DefaultSearchOptionsV2())
	if err == nil {
		t.Fatal("expected error when context is cancelled")
	}
	// The Go http client wraps the underlying ctx.Err in a url.Error; we
	// just assert *something* came back and it mentions the GET path.
	if !strings.Contains(err.Error(), "edge GET") {
		t.Errorf("error should be tagged with the request stage, got: %v", err)
	}
}

func TestBoolParam(t *testing.T) {
	if boolParam(true) != "1" {
		t.Errorf("boolParam(true) = %q, want 1", boolParam(true))
	}
	if boolParam(false) != "0" {
		t.Errorf("boolParam(false) = %q, want 0", boolParam(false))
	}
}

func TestFloatParam(t *testing.T) {
	tests := []struct {
		in   float64
		want string
	}{
		{0, "0.000000"},
		{0.5, "0.500000"},
		{1.0, "1.000000"},
		{1.23456789, "1.234568"}, // 6 decimal places
		{-0.5, "-0.500000"},
	}
	for _, tc := range tests {
		got := floatParam(tc.in)
		if got != tc.want {
			t.Errorf("floatParam(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
