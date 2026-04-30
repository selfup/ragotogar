# Architecture

The shape of the system today, plus what's still on the roadmap.

This is a living document — update when a phase ships or a decision changes.

---

## Current state (snapshot)

Four-stage Go pipeline against a single Postgres database with pgvector:

```
photos on disk
    ↓
[1] cmd/describe      vision LLM → photos / exif / descriptions / inference / thumbnails
    ↓
[2] cmd/classify      text LLM   → classified (typed enum fields from prose)
    ↓
[3] cmd/index         embedder   → chunks (halfvec(2560) + HNSW)
    ↓
[4] cmd/web           pgvector   → search UI (vector / vector+verify / FTS+vector / FTS+vector+verify)
    cmd/search        pgvector   → CLI for the same search path
```

All four `cmd/*` binaries plus the `library/` package run against one DSN (`LIBRARY_DSN`, default `postgres:///ragotogar`). Per-stage HTTP endpoints (`VISION_ENDPOINT`, `TEXT_ENDPOINT`, `EMBED_ENDPOINT`) let each pillar hit a different LLM provider; `LM_STUDIO_BASE` is the legacy shared fallback. All LLM calls go through `library/http.go`'s retry+backoff layer (5 attempts, exponential jitter, honors `Retry-After`, ctx-cancel aware).

Schema is Go-const + idempotent migrations (`migrate()` / `migrateV4()` / …) applied at process start by `cmd/describe` (the schema authority). Other binaries open the DB and assume tables exist.

---

## Why this changes at scale

At 30K+ photos:

| Pain | Magnitude |
|------|-----------|
| Re-describe wall-clock | Single-stream Qwen3-VL 8B ≈ 6s/photo → ~50 hours for 30K. Per-shard fanout cuts this proportionally. |
| Repeat search queries | Each LLM verify pass pays full inference cost; common queries should hit a cache |
| Single HNSW over 30K | Works, but per-shard indexes give faster incremental re-indexing and bound the noise floor per-query |
| Path-as-key fragility | `photos.id = name` today — file moves/renames break references that propagate through chunks and classified |
| Structured queries over prose | The `classified` table holds typed enums, but `cmd/web` doesn't yet route query tokens through them — "indoor scenes from a plane" still goes purely vector + FTS instead of `WHERE pov_container='from_plane' AND scene_indoor_outdoor='indoor'` |

The remaining phases address each.

---

## Target architecture

```
              ┌──────────────────────────────┐
              │  Search UI (web / CLI)       │
              └──────────────┬───────────────┘
                             ↓
              ┌──────────────────────────────┐
              │  Postgres + pgvector         │
              │  • photos / exif / inference / thumbnails
              │  • descriptions (prose + generated tsvector)
              │  • classified (typed enums from cmd/classify)
              │  • chunks (halfvec(2560), HNSW)
              │  • verify_cache (query × photo_id → verdict)         ← Phase 5
              └──────────────┬───────────────┘
                  ┌──────────┼──────────┐
                  ↓          ↓          ↓
              x100vi       xt5         a7      ← Phase 6: per-camera schemas for chunks
              .chunks    .chunks    .chunks      cross-shard query = UNION ALL
                  └──────────┼──────────┘
                             ↓
              ┌──────────────────────────────┐
              │  LLM verify (8-way parallel) │  ← Phase 5: cached in verify_cache
              └──────────────────────────────┘
```

### Six design pillars

`When` column reads "Now" if the implementation runs in tree today, or `Phase N` if it ships in a future phase.

| Pillar | What it solves | Implementation | When |
|--------|----------------|----------------|------|
| 1. Bounded-complexity vector indexes | Recall stability + per-shard re-index cost | Per-camera schemas keep each HNSW ~3-10K rows; cross-camera search via `UNION ALL` | Phase 6 |
| 2. Incremental re-indexing | Wall-clock cost of adding photos | `cmd/index` skip-if-exists by default; `-reindex` truncates and rebuilds | Now (per-photo); Phase 6 (per-shard) |
| 3. Forced parallel workloads | Throughput before LLM is involved | `library.VerifyConcurrency = 8`; `UNION ALL` across shards compounds the parallelism | Now (verify); Phase 6 (shards) |
| 4. Cache layer | Repeat-query cost | `verify_cache(query, photo_id, verdict)` keyed against shard `last_indexed_at` for invalidation | Phase 5 |
| 5. SQL frontend | Structured filters + cache lookup | `classified` table holds typed enums; `cmd/web` parses query tokens into WHERE predicates against `classified` + `exif` | Now (`classified` populated); Phase 4 (query routing) |
| 6. Single store for everything | Cache, portable moves, deep analysis | One Postgres DB holds all photo data including BLOB thumbnails; `pg_dump`/`pg_restore` (`scripts/db_dump.sh`, `scripts/db_restore.sh`) is the multi-machine sync workflow | Now |

---

## Key design decisions

These need to be pinned down before each relevant phase. Flagged with the phase that depends on them.

### A. Stable photo identity (Phase 7)

`file_path` is the de-facto key today. Files move → references through chunks/classified/photos break. Need a path-independent ID. The `photos.id` column already exists for this — Phase 7 populates it with a stable hash; until then it's the same string as `name`.

| Choice | Pros | Cons |
|--------|------|------|
| **SHA256 of file body** | True content identity; survives moves *and* renames; enables dedup detection | One-time hash cost (~minutes for 30K); changes if photo is re-edited |
| **Composite of EXIF date + filename + size** | Cheap; survives moves; survives reprocessing; doesn't read file body | Changes if EXIF is edited (rare); two distinct photos with identical EXIF+filename+size collide (vanishingly rare) |
| **UUID generated at describe time** | Cheap; stable through edits | Doesn't survive re-describe unless we propagate; no natural dedup |

**Default recommendation**: composite hash of `(exif_date_time_original, original_filename, file_size)`. Cheap, stable for the 99% case, no body read. Falls back to file body SHA256 if any composite component is missing.

### B. Shard key (Phase 6)

| Choice | Shard count | Pros | Cons |
|--------|-------------|------|------|
| `camera_model` (X100VI / X-T5 / A7…) | ~4-8 | Natural; aligns with how user thinks; fast camera-explicit routing | Imbalanced if one camera dominates |
| `make` (Fujifilm / Sony / DJI) | ~3-5 | Coarser; less imbalanced if user has multiple bodies of same brand | Loses model-level routing |
| `year` | ~5-10 | Time-balanced if shooting volume is roughly steady | Loses camera-explicit routing |
| Hybrid (`make+year`) | ~10-30 | Both axes bounded | Operational overhead grows with shard count |

**Default recommendation**: `camera_model` for the first cut. Re-evaluate if X100VI shard alone exceeds ~10K photos.

### C. Cache invalidation granularity (Phase 5)

| Choice | Pros | Cons |
|--------|------|------|
| Per-shard `last_indexed_at` invalidates all `(query, shard)` entries | Simple; matches re-index workflow | Coarse — adding one photo invalidates the whole shard's cache |
| Per-photo `(photo_id, query)` with photo timestamp | Surgical | More state, more complexity |

**Default recommendation**: per-shard. Matches the re-indexing model from pillar 2.

### D. Query-token parser bias (Phase 4)

`cmd/web`'s search function will extract structured tokens from the user's query and inject them as SQL predicates. Open question: how aggressive to be on inference.

| Choice | Pros | Cons |
|--------|------|------|
| Always-infer (parser quietly applies) | Best UX when classifier is right; user gets the obvious filter for free | When classifier mis-tagged a photo (e.g. labeled a sunset shot as `morning`), the user gets a confusingly empty result with no hint why |
| User-opt-in (UI chips for "indoor", "from_plane", etc.) | Predictable; user sees what's filtering | Loses the "natural language" feel — pushes the user back into faceted search |
| Hybrid (auto-infer with visible "filtering: X" chip + remove-X button) | Best of both — visible state, easy override | More UI surface |

**Default recommendation**: hybrid. Auto-infer the high-confidence tokens (camera names, year, aperture), surface them as removable chips so the user sees what's been applied and can drop a chip when the classifier is wrong.

---

## What's left

Each phase delivers value independently. No phase is gated on a later one, but they compose.

| Phase | Delivers | Effort |
|-------|----------|--------|
| **4. SQL pre-filter routing** | `cmd/web` parses query → injects WHERE predicates against `classified` + `exif`; vector + RRF + verify run over the filtered set | small |
| **5. Verify-result cache** | `verify_cache(query, photo_id, verdict)` in same DB; repeat queries free | small |
| **6. Camera-sharded chunks** | `x100vi.chunks`, `xt5.chunks`, …; cross-shard search = `UNION ALL`; per-shard re-index = `TRUNCATE shard.chunks; INSERT …` | small |
| **7. Stable photo IDs** | Composite hash on `photos.id`; move/rename safe; dedup detection | medium |
| **8. Cross-shard analysis tooling** | `cmd/analyze` subcommands — lens diversity, aperture distribution, time-of-day heatmap, classifier vocabulary | small |

Highest near-term leverage: **Phase 4** — the typed signal already exists in `classified`, only the query routing is missing.

---

## Phase notes (sketches, not specs)

### Phase 4: SQL pre-filter in the search path

`classified` table is populated; turning it into search filters is mechanical:

1. **Parse the query** for tokens that map to columns:
   - `indoor` / `outdoor` → `classified.scene_indoor_outdoor`
   - `morning` / `noon` / `evening` / `night` → `classified.scene_time_of_day`
   - `from a plane` / `from a car` / `from a balcony` → `classified.pov_container`
   - `aerial` / `ground level` / `low altitude` → `classified.pov_altitude`
   - `single` / `pair` / `crowd` → `classified.subject_count`
   - camera names (`X100VI`, `X-T5`) → `exif.camera_model`
   - year mentions (`2024`, `April 2024`) → `exif.date_taken_year` (+ month)
   - aperture-shaped substrings (`f/2.8`) → `exif.f_number BETWEEN`
2. **Strip the parsed tokens** from the residual embedding query so vector doesn't try to match "indoor" twice
3. **Single SQL roundtrip**:
   ```sql
   SELECT name, MAX(1 - (c.embedding <=> $1)) AS similarity
   FROM chunks c
   JOIN photos p ON p.id = c.photo_id
   LEFT JOIN classified k ON k.photo_id = p.id
   LEFT JOIN exif e ON e.photo_id = p.id
   WHERE [parser-built predicates]
   GROUP BY name
   ORDER BY similarity DESC
   LIMIT $2;
   ```
4. **Surface the filters**: emit removable chips in the UI ("Filtering: indoor · from_plane · X100VI") so the user can drop a chip when the classifier mis-tagged a photo

Existing FTS+RRF arm continues to operate over the same filtered set.

### Phase 5: Verify cache

```sql
CREATE TABLE verify_cache (
    query             TEXT NOT NULL,
    photo_id          TEXT NOT NULL REFERENCES photos(id) ON DELETE CASCADE,
    verdict           BOOLEAN NOT NULL,
    verified_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    shard_indexed_at  TIMESTAMPTZ,        -- for invalidation against shard re-indexes
    PRIMARY KEY (query, photo_id)
);
```

Lookup: `(query, photo_id)` and `verified_at > shards.last_indexed_at[shard_of_photo]`. Hit → skip the LLM call. Cold → run verify, write the verdict.

Open: how to canonicalize `query` for cache hits. Lower-case + trim is cheap; semantic dedupe (embedding-similar queries hit the same cache row) is overkill until we have evidence the user re-types queries with cosmetic variation.

### Phase 6: Camera-sharded chunks

- `chunks` moves from `public` schema into per-camera schemas: `x100vi.chunks`, `xt5.chunks`, `z8.chunks`, …
- New `public.shards (name, schema, last_indexed_at, photo_count)` registry
- Cross-shard query: `SELECT … FROM x100vi.chunks UNION ALL SELECT … FROM xt5.chunks ORDER BY similarity DESC LIMIT k`
- Per-shard re-index: `TRUNCATE x100vi.chunks; INSERT …` — bounded to one camera's data; doesn't disturb the other shards

The `photos` / `exif` / `descriptions` / `classified` / `inference` / `thumbnails` tables stay in `public` — they're the photo registry, not shard data. Only `chunks` shards because vectors are the only table where index rebuild dominates wall-clock.

### Phase 7: Stable photo IDs

- Compute composite hash on next describe run (cheap — just EXIF + name + size)
- Migration: walk existing photos, populate `id` from composite, leaving `name` as the human-readable handle
- `chunks` / `classified` / `inference` / `thumbnails` keep their `photo_id` FK semantics; if `id` is the FK and `name` is just the display string, the FK is stable across renames and moves
- No re-embed required — embeddings keyed on `id`

### Phase 8: Cross-shard analysis tooling

`cmd/analyze` (or root-module package) with subcommands:

- `lens-stats` — most-used lenses by year (`exif.lens_model` × `exif.date_taken_year`)
- `aperture-distribution` — histogram per camera (`exif.f_number` GROUP BY)
- `coverage-by-camera-month` — heatmap data (cross-tabulate)
- `pov-breakdown` — `classified.pov_container` distribution per camera (was the X100VI mostly handheld? was the Z8 mostly aerial?)
- `vocabulary` — most common tokens in the FTS index for prose-feel auditing
- `classifier-disagreement` — photos where classifier output looks suspicious (flag for re-classify with stronger model)

Outputs Markdown tables / JSON; consumed by future "library dashboard" web view.
