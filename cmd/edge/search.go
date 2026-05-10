package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"ragotogar/library"
)

// server holds the shared runtime state — artifacts (mmap'd, lifetime
// of the process) and a pg handle for hydration. The handler methods
// hang off it so request-scoped state is the only thing that flows
// through HTTP.
type server struct {
	arts *Artifacts
	mux  *http.ServeMux
}

// searchRequest captures the URL params for /search after parsing.
// Defaults match cmd/web's six-mode UI as documented in EDGE.md.
type searchRequest struct {
	Query string

	UseDescriptions bool
	UseMetadata     bool
	UseQueries      bool

	Merge   MergeStrategy
	Wd      float64
	Wm      float64
	Wq      float64
	Cosine  float64
	UseFST  bool
	UseVec  bool
	TopK    int
}

func parseSearchRequest(r *http.Request) (searchRequest, error) {
	q := r.URL.Query().Get("q")
	if q == "" {
		return searchRequest{}, fmt.Errorf("q is required")
	}
	if ContainsPhrase(q) {
		return searchRequest{}, fmt.Errorf(`phrase queries (quoted) are not supported at the edge — the FST has no position info, so adjacency can't be reproduced. Drop the quotes or query cmd/web for phrase support`)
	}

	req := searchRequest{
		Query:           q,
		UseDescriptions: paramBool(r, "descriptions", true),
		UseMetadata:     paramBool(r, "metadata", true),
		UseQueries:      paramBool(r, "queries", true),
		Merge:           MergeStrategy(paramString(r, "merge", string(MergeUnion))),
		Wd:              paramFloat(r, "wd", 1.0),
		Wm:              paramFloat(r, "wm", 1.0),
		Wq:              paramFloat(r, "wq", 1.0),
		Cosine:          paramFloat(r, "cosine", 0.50),
		UseFST:          paramBool(r, "lexical", true),
		UseVec:          paramBool(r, "vector", true),
		// TopK=0 means unbounded — cosine threshold is the only bound,
		// matching cmd/web's behavior. Direct API callers who want a
		// smaller response can pass ?topk=N to truncate post-fusion.
		TopK:            int(paramFloat(r, "topk", 0)),
	}
	if !req.UseFST && !req.UseVec {
		return searchRequest{}, fmt.Errorf("at least one of vector=1 or lexical=1 must be set")
	}
	if !req.UseDescriptions && !req.UseMetadata && !req.UseQueries && req.UseVec {
		return searchRequest{}, fmt.Errorf("vector=1 requires at least one of descriptions/metadata/queries")
	}
	return req, nil
}

func paramBool(r *http.Request, key string, def bool) bool {
	v := r.URL.Query().Get(key)
	if v == "" {
		return def
	}
	switch v {
	case "1", "true", "on", "yes":
		return true
	case "0", "false", "off", "no":
		return false
	default:
		return def
	}
}

func paramString(r *http.Request, key, def string) string {
	v := r.URL.Query().Get(key)
	if v == "" {
		return def
	}
	return v
}

func paramFloat(r *http.Request, key string, def float64) float64 {
	v := r.URL.Query().Get(key)
	if v == "" {
		return def
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return def
	}
	return f
}

// searchResponse is what /search emits as JSON. Per-arm timing is
// inline so callers see the shape of the work without consulting
// logs — useful for parity validation.
type searchResponse struct {
	Query         string      `json:"query"`
	StrippedQuery string      `json:"stripped_query"`
	Negation      string      `json:"negation"`
	ElapsedMs     float64     `json:"elapsed_ms"`
	VectorArm     armReport   `json:"vector_arm"`
	FSTArm        armReport   `json:"fst_arm"`
	FusedTotal    int         `json:"fused_total"`
	AfterNegation int         `json:"after_negation"`
	Hits          []searchHit `json:"hits"`
}

type armReport struct {
	Enabled   bool    `json:"enabled"`
	Results   int     `json:"results"`
	ElapsedMs float64 `json:"elapsed_ms"`
}

type searchHit struct {
	CompactID uint32            `json:"compact_id"`
	Name      string            `json:"name"`
	Caption   string            `json:"caption"`
	Tags      map[string]string `json:"tags"`
	Score     float64           `json:"score"`
}

func (s *server) handleSearch(w http.ResponseWriter, r *http.Request) {
	req, err := parseSearchRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	resp := searchResponse{
		Query:         req.Query,
		StrippedQuery: library.StripNegation(req.Query),
		Negation:      library.ExtractNegation(req.Query),
	}
	resp.VectorArm.Enabled = req.UseVec
	resp.FSTArm.Enabled = req.UseFST

	start := time.Now()

	// Vector arm.
	var vectorHits []LaneHit
	if req.UseVec {
		armStart := time.Now()
		// Embed the positive residual.
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		embeddings, err := library.EmbedTexts(ctx, []string{resp.StrippedQuery})
		if err != nil {
			http.Error(w, fmt.Sprintf("embed query: %v", err), http.StatusBadGateway)
			return
		}
		queryInt8 := quantizeQueryInt8(embeddings[0])

		// Per-enabled-lane scan.
		perLane := map[string][]LaneHit{}
		if req.UseDescriptions {
			h, err := s.arts.ScanLane("descriptions", queryInt8, req.Cosine)
			if err != nil {
				http.Error(w, fmt.Sprintf("scan descriptions: %v", err), http.StatusInternalServerError)
				return
			}
			perLane["descriptions"] = h
		}
		if req.UseMetadata {
			h, err := s.arts.ScanLane("metadata", queryInt8, req.Cosine)
			if err != nil {
				http.Error(w, fmt.Sprintf("scan metadata: %v", err), http.StatusInternalServerError)
				return
			}
			perLane["metadata"] = h
		}
		if req.UseQueries {
			h, err := s.arts.ScanLane("queries", queryInt8, req.Cosine)
			if err != nil {
				http.Error(w, fmt.Sprintf("scan queries: %v", err), http.StatusInternalServerError)
				return
			}
			perLane["queries"] = h
		}

		vectorHits = MergeStores(perLane, MergeOptions{
			Strategy:           req.Merge,
			WeightDescriptions: req.Wd,
			WeightMetadata:     req.Wm,
			WeightQueries:      req.Wq,
		})
		resp.VectorArm.Results = len(vectorHits)
		resp.VectorArm.ElapsedMs = elapsedMs(armStart)
	}

	// FST arm.
	var fstHits []LaneHit
	if req.UseFST {
		armStart := time.Now()
		fstHits, err = s.arts.ScanFST(resp.StrippedQuery)
		if err != nil {
			http.Error(w, fmt.Sprintf("FST scan: %v", err), http.StatusInternalServerError)
			return
		}
		resp.FSTArm.Results = len(fstHits)
		resp.FSTArm.ElapsedMs = elapsedMs(armStart)
	}

	// Fuse.
	var fused []LaneHit
	switch {
	case req.UseVec && req.UseFST:
		fused = RRFFuse(vectorHits, fstHits)
	case req.UseVec:
		fused = vectorHits
	case req.UseFST:
		fused = fstHits
	}
	resp.FusedTotal = len(fused)

	// Negation post-filter.
	if resp.Negation != "" {
		drop, err := s.arts.FSTNegationDrop(resp.Negation)
		if err != nil {
			http.Error(w, fmt.Sprintf("negation drop: %v", err), http.StatusInternalServerError)
			return
		}
		fused = FilterByDropSet(fused, drop)
	}
	resp.AfterNegation = len(fused)

	// TopK truncate.
	if req.TopK > 0 && len(fused) > req.TopK {
		fused = fused[:req.TopK]
	}

	// Hydrate name + payload per hit.
	resp.Hits = make([]searchHit, 0, len(fused))
	for _, h := range fused {
		if int(h.CompactID) >= len(s.arts.Manifest.IDSpace.Names) {
			continue
		}
		caption, tagVals, err := s.arts.DecodePayload(h.CompactID)
		if err != nil {
			// Don't fail the whole response on one bad payload row;
			// emit a hit with empty payload and keep going.
			caption = ""
			tagVals = make([]string, len(s.arts.Manifest.Payload.Tags))
		}
		tags := map[string]string{}
		for i, name := range s.arts.Manifest.Payload.Tags {
			if i < len(tagVals) {
				tags[name] = tagVals[i]
			}
		}
		resp.Hits = append(resp.Hits, searchHit{
			CompactID: h.CompactID,
			Name:      s.arts.Manifest.IDSpace.Names[h.CompactID],
			Caption:   caption,
			Tags:      tags,
			Score:     h.Similarity,
		})
	}

	resp.ElapsedMs = elapsedMs(start)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(&resp)
}

func elapsedMs(start time.Time) float64 {
	return float64(time.Since(start).Microseconds()) / 1000.0
}
