package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"ragotogar/library"
)

// edgeSearchResponse mirrors cmd/edge's /search JSON shape. We only
// destructure the fields cmd/web needs for parity comparison —
// per-arm timing and payload (caption/tags) are intentionally not
// surfaced through the existing render path; the Name + Score map
// directly into library.Result and that's what the grid template
// already renders.
type edgeSearchResponse struct {
	Hits []struct {
		Name  string  `json:"name"`
		Score float64 `json:"score"`
	} `json:"hits"`
}

// retrieveFromEdge GETs cmd/edge's /search endpoint using the same
// URL parameter shape cmd/edge expects (which mirrors cmd/web's own
// URL contract by design — see EDGE.md). Translates the response to
// []library.Result for the existing dispatcher in search.go.
//
// mode controls whether the FST arm participates: cmd/edge's lexical=0
// disables it for "vector"-only modes, lexical=1 enables it for
// "fts-vector". Verify and auto modes don't reach this function — the
// rewrite step runs upstream of retrieval and verify runs downstream,
// both in cmd/web against pg.
//
// Phrase queries (any quote in the query) cause cmd/edge to return
// HTTP 400; that body is surfaced verbatim as the error so the UI
// status line shows the user what to do.
func retrieveFromEdge(ctx context.Context, edgeURL string, mode, query string, opts library.SearchOptionsV2) ([]library.Result, error) {
	if edgeURL == "" {
		return nil, fmt.Errorf("edge URL not configured")
	}

	params := url.Values{}
	params.Set("q", query)
	params.Set("descriptions", boolParam(opts.UseDescriptions))
	params.Set("metadata", boolParam(opts.UseMetadata))
	params.Set("queries", boolParam(opts.UseQueries))
	params.Set("merge", string(opts.MergeStrategy))
	params.Set("wd", floatParam(opts.WeightDescriptions))
	params.Set("wm", floatParam(opts.WeightMetadata))
	params.Set("wq", floatParam(opts.WeightQueries))
	if opts.Threshold != nil {
		params.Set("cosine", floatParam(*opts.Threshold))
	}

	// Mode → lexical/vector arm routing. cmd/web's "naive" /
	// "naive-verify" modes are vector-only; the FTS+vector modes
	// engage cmd/edge's FST arm.
	switch mode {
	case "fts-vector", "fts-vector-verify":
		params.Set("vector", "1")
		params.Set("lexical", "1")
	default:
		params.Set("vector", "1")
		params.Set("lexical", "0")
	}

	target := edgeURL + "/search?" + params.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("edge GET: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("edge read body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		// cmd/edge returns plain-text errors (e.g. the phrase 400).
		// Surface verbatim so the UI status line is actionable.
		return nil, fmt.Errorf("edge %d: %s", resp.StatusCode, string(body))
	}

	var out edgeSearchResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("edge decode: %w", err)
	}
	results := make([]library.Result, len(out.Hits))
	for i, h := range out.Hits {
		results[i] = library.Result{Name: h.Name, Similarity: h.Score}
	}
	return results, nil
}

func boolParam(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

func floatParam(f float64) string {
	return strconv.FormatFloat(f, 'f', 6, 64)
}
