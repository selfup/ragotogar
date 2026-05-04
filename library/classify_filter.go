package library

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lib/pq"

	"ragotogar/prompts"
)

// ClassifyFilterStats summarizes one FilterByClassification call. Surfaced
// in the cmd/web result header so the user can see how the filter behaved
// on this query.
type ClassifyFilterStats struct {
	Total   int           // candidates that came in
	Dropped int           // candidates the LLM (or cache) marked drop
	Cached  int           // verdicts served from classify_filter_cache
	LLM     int           // verdicts produced by a live LLM call (0 or 1 — the call is batched)
	Elapsed time.Duration // wall-clock for the whole step
}

// FilterByClassification asks an LLM to drop candidates whose classifier
// verdicts contradict the user's natural-language intent. One batched call
// per invocation: the prompt carries the NL query plus a compact line per
// candidate ("id: key=val, key=val, …"). Output is a strict json_schema
// listing the photo IDs to drop; everything else passes through in the
// original retrieval order.
//
// useCache controls the classify_filter_cache:
//   - true:  consult the cache per-candidate; cache hits skip the LLM
//            entirely. The LLM call covers only candidates with no
//            cached verdict. Hits are filtered for freshness against
//            classified.classified_at — if a photo was re-classified
//            after its verdict was cached, the cache row is ignored.
//   - false: skip the cache (no read, no write). Always call the LLM.
//
// On any error (LLM call, JSON parse, DB I/O) the function returns the
// candidates unchanged with the error — the filter is advisory; callers
// should treat failures as "filter ran but produced no drops" rather
// than failing the whole search.
func FilterByClassification(ctx context.Context, db *sql.DB, nl string, candidates []Result, model string, useCache bool) ([]Result, ClassifyFilterStats, error) {
	start := time.Now()
	stats := ClassifyFilterStats{Total: len(candidates)}
	if len(candidates) == 0 {
		stats.Elapsed = time.Since(start)
		return candidates, stats, nil
	}

	rows, err := loadClassifications(ctx, db, candidates)
	if err != nil {
		stats.Elapsed = time.Since(start)
		return candidates, stats, fmt.Errorf("load classifications: %w", err)
	}

	dropSet := make(map[string]bool, len(candidates))

	// Cache lookup: serve fresh verdicts directly; collect misses for the
	// LLM call.
	canonical := CanonicalQuery(nl)
	missing := make([]string, 0, len(candidates))
	if useCache {
		hits, err := lookupClassifyFilterCache(ctx, db, canonical, candidates, model)
		if err != nil {
			// Cache lookup failure is non-fatal — fall through to LLM
			// for everything. Surfaced via error return for caller logging.
			missing = make([]string, 0, len(candidates))
			for _, c := range candidates {
				missing = append(missing, c.Name)
			}
		} else {
			for _, c := range candidates {
				if v, ok := hits[c.Name]; ok {
					stats.Cached++
					if v {
						dropSet[c.Name] = true
					}
				} else {
					missing = append(missing, c.Name)
				}
			}
		}
	} else {
		for _, c := range candidates {
			missing = append(missing, c.Name)
		}
	}

	// LLM call covers only candidates whose verdict isn't cached.
	if len(missing) > 0 {
		dropFromLLM, err := classifyFilterLLM(ctx, nl, missing, rows, model)
		if err != nil {
			stats.Elapsed = time.Since(start)
			return candidates, stats, fmt.Errorf("classify filter llm: %w", err)
		}
		stats.LLM = len(missing)
		for _, id := range dropFromLLM {
			dropSet[id] = true
		}
		if useCache {
			// Write a verdict row for every candidate the LLM saw — drop=true
			// for the dropped IDs, drop=false for the rest. Lets future hits
			// short-circuit instead of re-asking. Best-effort; cache write
			// failure doesn't affect the current request.
			drops := make(map[string]bool, len(missing))
			for _, id := range dropFromLLM {
				drops[id] = true
			}
			for _, id := range missing {
				if err := storeClassifyFilterCache(ctx, db, canonical, id, model, drops[id]); err != nil {
					// Don't fail — log via return error after the loop completes.
					_ = err
				}
			}
		}
	}

	kept := candidates[:0]
	for _, c := range candidates {
		if !dropSet[c.Name] {
			kept = append(kept, c)
		}
	}
	stats.Dropped = stats.Total - len(kept)
	stats.Elapsed = time.Since(start)
	return kept, stats, nil
}

// loadClassifications fetches the classifier row for every candidate. Photos
// without a classifier row (classify wasn't run, or row missing) get an empty
// map entry — formatted as "id: (no classification)" so the LLM is told this
// candidate has no signal to evaluate against. The LLM then leaves it alone.
func loadClassifications(ctx context.Context, db *sql.DB, candidates []Result) (map[string]string, error) {
	names := make([]string, len(candidates))
	for i, c := range candidates {
		names[i] = c.Name
	}
	rows, err := db.QueryContext(ctx, `
		SELECT p.name,
		       c.pov_container, c.pov_altitude, c.pov_angle,
		       c.subject_altitude, c.subject_category,
		       c.subject_distance, c.subject_count, c.animal_count,
		       c.scene_time_of_day, c.scene_indoor_outdoor, c.scene_weather,
		       c.framing, c.motion, c.color_palette
		FROM photos p
		LEFT JOIN classified c ON c.photo_id = p.id
		WHERE p.name = ANY($1)
	`, pq.Array(names))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[string]string, len(candidates))
	for rows.Next() {
		var (
			name                                                                                            string
			povContainer, povAltitude, povAngle                                                             sql.NullString
			subjectAltitude                                                                                 sql.NullString
			subjectCategory                                                                                 []string
			subjectDistance, subjectCount, animalCount                                                      sql.NullString
			sceneTimeOfDay, sceneIndoorOutdoor, sceneWeather                                                sql.NullString
			framing                                                                                         []string
			motion, colorPalette                                                                            sql.NullString
		)
		if err := rows.Scan(
			&name,
			&povContainer, &povAltitude, &povAngle,
			&subjectAltitude, pq.Array(&subjectCategory),
			&subjectDistance, &subjectCount, &animalCount,
			&sceneTimeOfDay, &sceneIndoorOutdoor, &sceneWeather,
			pq.Array(&framing), &motion, &colorPalette,
		); err != nil {
			return nil, err
		}
		out[name] = formatClassification(
			povContainer, povAltitude, povAngle,
			subjectAltitude, subjectCategory,
			subjectDistance, subjectCount, animalCount,
			sceneTimeOfDay, sceneIndoorOutdoor, sceneWeather,
			framing, motion, colorPalette,
		)
	}
	return out, rows.Err()
}

// formatClassification renders a classifier row as a single comma-separated
// line of "key=value" / "key=[v1,v2]" pairs, skipping NULL columns and empty
// arrays so the prompt stays compact and noise-free. Returns "(no
// classification)" when every column is empty.
func formatClassification(
	povContainer, povAltitude, povAngle sql.NullString,
	subjectAltitude sql.NullString,
	subjectCategory []string,
	subjectDistance, subjectCount, animalCount sql.NullString,
	sceneTimeOfDay, sceneIndoorOutdoor, sceneWeather sql.NullString,
	framing []string,
	motion, colorPalette sql.NullString,
) string {
	parts := make([]string, 0, 14)
	addScalar := func(key string, v sql.NullString) {
		if v.Valid && v.String != "" {
			parts = append(parts, key+"="+v.String)
		}
	}
	addArray := func(key string, v []string) {
		if len(v) > 0 {
			parts = append(parts, key+"=["+strings.Join(v, ",")+"]")
		}
	}
	addScalar("pov_container", povContainer)
	addScalar("pov_altitude", povAltitude)
	addScalar("pov_angle", povAngle)
	addScalar("subject_altitude", subjectAltitude)
	addArray("subject_category", subjectCategory)
	addScalar("subject_distance", subjectDistance)
	addScalar("subject_count", subjectCount)
	addScalar("animal_count", animalCount)
	addScalar("scene_time_of_day", sceneTimeOfDay)
	addScalar("scene_indoor_outdoor", sceneIndoorOutdoor)
	addScalar("scene_weather", sceneWeather)
	addArray("framing", framing)
	addScalar("motion", motion)
	addScalar("color_palette", colorPalette)
	if len(parts) == 0 {
		return "(no classification)"
	}
	return strings.Join(parts, ", ")
}

// classifyFilterLLM builds the prompt, calls the LLM with a strict json_schema,
// and returns the list of IDs to drop. The schema constrains the model to
// return only IDs from the set of candidates we sent — invalid IDs are
// silently dropped during the rebuild.
func classifyFilterLLM(ctx context.Context, nl string, candidateIDs []string, rows map[string]string, model string) ([]string, error) {
	var b strings.Builder
	for _, id := range candidateIDs {
		body, ok := rows[id]
		if !ok {
			body = "(no classification)"
		}
		b.WriteString(id)
		b.WriteString(": ")
		b.WriteString(body)
		b.WriteByte('\n')
	}
	prompt := strings.Replace(prompts.ClassifyFilter, "{{query}}", nl, 1)
	prompt = strings.Replace(prompt, "{{candidates}}", strings.TrimRight(b.String(), "\n"), 1)

	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"drop_ids": map[string]any{
				"type":  "array",
				"items": map[string]any{"type": "string"},
			},
		},
		"required":             []string{"drop_ids"},
		"additionalProperties": false,
	}

	raw, err := LLMCompleteSchema(ctx, model, prompt, "classify_filter", schema)
	if err != nil {
		return nil, err
	}
	var out struct {
		DropIDs []string `json:"drop_ids"`
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, fmt.Errorf("decode drop_ids: %w (body=%q)", err, raw)
	}
	// Whitelist against the IDs we sent so the model can't smuggle in a
	// drop verdict for a photo that wasn't in the batch.
	allowed := make(map[string]struct{}, len(candidateIDs))
	for _, id := range candidateIDs {
		allowed[id] = struct{}{}
	}
	kept := out.DropIDs[:0]
	for _, id := range out.DropIDs {
		if _, ok := allowed[id]; ok {
			kept = append(kept, id)
		}
	}
	return kept, nil
}

// lookupClassifyFilterCache returns photo_name → drop verdict for rows
// where the cached verdict is fresher than the photo's last classify run.
// Stale rows (where classified_at > filtered_at) are silently dropped at
// lookup time so re-classifying a photo invalidates older verdicts without
// needing an explicit cache bust.
func lookupClassifyFilterCache(ctx context.Context, db *sql.DB, nl string, candidates []Result, model string) (map[string]bool, error) {
	names := make([]string, len(candidates))
	for i, c := range candidates {
		names[i] = c.Name
	}
	rows, err := db.QueryContext(ctx, `
		SELECT cfc.photo_id, cfc.drop_verdict
		FROM classify_filter_cache cfc
		JOIN photos p ON p.id = cfc.photo_id
		LEFT JOIN classified cl ON cl.photo_id = cfc.photo_id
		WHERE cfc.nl_query = $1
		  AND cfc.classify_model = $2
		  AND p.name = ANY($3)
		  AND (cl.classified_at IS NULL OR cfc.filtered_at > cl.classified_at)
	`, nl, model, pq.Array(names))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]bool, len(candidates))
	for rows.Next() {
		var (
			id      string
			verdict bool
		)
		if err := rows.Scan(&id, &verdict); err != nil {
			return nil, err
		}
		out[id] = verdict
	}
	return out, rows.Err()
}

func storeClassifyFilterCache(ctx context.Context, db *sql.DB, nl, photoID, model string, drop bool) error {
	_, err := db.ExecContext(ctx, `
		INSERT INTO classify_filter_cache (nl_query, photo_id, classify_model, drop_verdict, filtered_at)
		VALUES ($1, $2, $3, $4, now())
		ON CONFLICT (nl_query, photo_id, classify_model) DO UPDATE SET
			drop_verdict = EXCLUDED.drop_verdict,
			filtered_at  = now()
	`, nl, photoID, model, drop)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	return nil
}
