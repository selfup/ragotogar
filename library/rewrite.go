package library

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"ragotogar/prompts"
)

// RewriteResult is the outcome of a single RewriteQuery call. Original is the
// raw user query; Rewritten is the websearch_to_tsquery boolean form (or the
// raw query unchanged when the LLM call fails / returns empty / the rewrite
// is degenerate). Cached indicates the rewrite came from query_rewrite_cache
// rather than a live LLM call.
type RewriteResult struct {
	Original  string
	Rewritten string
	Cached    bool
	Elapsed   time.Duration
}

// RewriteQuery translates a natural-language photo search into Postgres
// websearch_to_tsquery boolean form via the text-endpoint LLM. Cache
// behavior is controlled by useCache:
//
//   - useCache=true: read query_rewrite_cache; validate hits before reuse.
//     On miss, call the LLM with a bounded output budget and cache valid
//     results. Invalid cached entries are evicted and fall back to the input.
//   - useCache=false: skip the DB entirely (no read, no write). Always
//     call the LLM. Use this for iterate-mode where the
//     user wants fresh output until they're satisfied.
//
// The rewrite is advisory. LLM errors, invalid output, and cache lookup errors
// return the raw query in Rewritten, with an error for logging. Cache-write
// errors retain the validated rewrite so retrieval can still use it.
//
// The function also short-circuits when the user has clearly already typed
// a boolean query (contains a leading `-`, a quoted phrase, or uppercase
// `OR`). Re-running the rewrite over an already-boolean query risks the
// LLM "fixing" something that's already correct; pass-through is safer.
func RewriteQuery(ctx context.Context, db *sql.DB, nl, model string, useCache bool) (RewriteResult, error) {
	start := time.Now()
	res := RewriteResult{Original: nl, Rewritten: nl}

	if looksBoolean(nl) {
		res.Elapsed = time.Since(start)
		return res, nil
	}

	canonical := CanonicalQuery(nl)
	if useCache {
		if cached, ok, err := lookupRewriteCache(ctx, db, canonical, model); err != nil {
			// Fall back to the original query on cache I/O errors.
			res.Elapsed = time.Since(start)
			return res, fmt.Errorf("rewrite cache lookup: %w", err)
		} else if ok {
			if cached != nl {
				if err := validateRewrite(nl, cached); err != nil {
					// Do not reuse bad output saved by older versions. Match the
					// value too, so a concurrent valid replacement survives.
					_, _ = db.ExecContext(ctx, `DELETE FROM query_rewrite_cache
						WHERE nl_query = $1 AND rewrite_model = $2 AND rewritten = $3`, canonical, model, cached)
					res.Elapsed = time.Since(start)
					return res, fmt.Errorf("invalid cached rewrite: %w", err)
				}
			}
			res.Rewritten = cached
			res.Cached = true
			res.Elapsed = time.Since(start)
			return res, nil
		}
	}

	prompt := strings.Replace(prompts.Query, "{{query}}", nl, 1)
	rewritten, err := llmCompleteWithLimit(ctx, model, prompt, nil, 256)
	if err != nil {
		res.Elapsed = time.Since(start)
		return res, fmt.Errorf("rewrite llm: %w", err)
	}
	// Validate the complete response before sanitizing so taking its first
	// line cannot disguise a runaway response from a noncompliant endpoint.
	if err := validateRewrite(nl, rewritten); err != nil {
		res.Elapsed = time.Since(start)
		return res, fmt.Errorf("invalid rewrite: %w", err)
	}
	rewritten = sanitizeRewrite(rewritten)
	if err := validateRewrite(nl, rewritten); err != nil {
		res.Elapsed = time.Since(start)
		return res, fmt.Errorf("invalid rewrite: %w", err)
	}
	if rewritten == nl {
		// An unchanged valid query may be cached, but rejected output never is.
		if useCache {
			_ = storeRewriteCache(ctx, db, canonical, model, nl)
		}
		res.Elapsed = time.Since(start)
		return res, nil
	}

	res.Rewritten = rewritten
	if useCache {
		if err := storeRewriteCache(ctx, db, canonical, model, rewritten); err != nil {
			res.Elapsed = time.Since(start)
			return res, fmt.Errorf("rewrite cache store: %w", err)
		}
	}
	res.Elapsed = time.Since(start)
	return res, nil
}

// validateRewrite bounds generated output without assuming English. Detect
// repetition both between words and inside unsegmented text (e.g. 飞机飞机…).
func validateRewrite(original, rewritten string) error {
	runes := []rune(rewritten)
	limit := min(512, max(128, utf8.RuneCountInString(original)*8))
	if len(runes) > limit {
		return fmt.Errorf("output exceeds %d characters", limit)
	}
	words := strings.FieldsFunc(strings.ToLower(rewritten), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})
	if len(words) == 0 {
		return errors.New("output contains no search terms")
	}
	counts := map[string]int{}
	for _, word := range words {
		counts[word]++
		if word != "or" && counts[word] >= 4 && counts[word]*2 >= len(words) {
			return errors.New("output repeats the same search term")
		}
	}
	for width := 1; width <= 32; width++ {
		for start := 0; start+width*6 <= len(runes); start++ {
			repeated := true
			for i := width; i < width*6; i++ {
				if runes[start+i] != runes[start+i%width] {
					repeated = false
					break
				}
			}
			if repeated && strings.TrimSpace(string(runes[start:start+width])) != "" {
				return errors.New("output contains a repeated character sequence")
			}
		}
	}
	if strings.Count(rewritten, `"`)%2 != 0 {
		return errors.New("output contains an unfinished quoted phrase")
	}
	return nil
}

// looksBoolean returns true when the query already contains websearch
// operator syntax — quoted phrases, leading-dash negation, or uppercase OR.
// These get short-circuited past the rewrite so the LLM can't "improve"
// queries the user already wrote in boolean form.
func looksBoolean(q string) bool {
	if strings.Contains(q, `"`) {
		return true
	}
	if strings.Contains(q, " OR ") || strings.HasPrefix(q, "OR ") || strings.HasSuffix(q, " OR") {
		return true
	}
	for f := range strings.FieldsSeq(q) {
		if strings.HasPrefix(f, "-") && len(f) > 1 {
			return true
		}
	}
	return false
}

// sanitizeRewrite cleans the LLM output. The prompt asks for one-line raw
// output, but smaller models will sometimes prepend a "Rewritten:" prefix
// or wrap the result in code fences. Trim those rather than fail outright.
func sanitizeRewrite(s string) string {
	s = strings.TrimSpace(s)
	// Strip code fences (``` or ```text)
	s = strings.TrimPrefix(s, "```text")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	s = strings.TrimSpace(s)
	// Strip a leading "Rewritten:" / "rewrite:" / "Output:" label
	for _, prefix := range []string{"Rewritten:", "rewritten:", "Rewrite:", "rewrite:", "Output:", "output:"} {
		if rest, ok := strings.CutPrefix(s, prefix); ok {
			s = strings.TrimSpace(rest)
			break
		}
	}
	// Take only the first line — multi-line responses are model drift; the
	// useful query is on line one.
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	return s
}

func lookupRewriteCache(ctx context.Context, db *sql.DB, nl, model string) (string, bool, error) {
	var rewritten string
	err := db.QueryRowContext(ctx, `
		SELECT rewritten FROM query_rewrite_cache
		WHERE nl_query = $1 AND rewrite_model = $2
	`, nl, model).Scan(&rewritten)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", false, nil
		}
		return "", false, err
	}
	return rewritten, true, nil
}

func storeRewriteCache(ctx context.Context, db *sql.DB, nl, model, rewritten string) error {
	_, err := db.ExecContext(ctx, `
		INSERT INTO query_rewrite_cache (nl_query, rewrite_model, rewritten, rewritten_at)
		VALUES ($1, $2, $3, now())
		ON CONFLICT (nl_query, rewrite_model) DO UPDATE SET
			rewritten    = EXCLUDED.rewritten,
			rewritten_at = now()
	`, nl, model, rewritten)
	return err
}
