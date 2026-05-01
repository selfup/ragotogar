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

Schema is Go-const + idempotent migrations (`migrate()` / `migrateV4()` / `migrateV6()` / `migrateV7()`) applied at process start by `cmd/describe` (the schema authority). Other binaries open the DB and assume tables exist.

The verify pass consults `verify_cache(query, photo_id, verify_model, verdict, verified_at)` before each LLM round-trip. Lookup is one batch SQL roundtrip; rows where `verified_at > inference.described_at` count as fresh. Re-describing a photo silently invalidates older cached verdicts without explicit teardown. Cache hit rate is logged to stderr by `cmd/search` and surfaced in the `cmd/web` results header so the user can watch it climb across repeat queries.

---

## Why this changes at scale

At 30K+ photos:

| Pain | Magnitude |
|------|-----------|
| Re-describe wall-clock | Single-stream Qwen3-VL 8B ≈ 6s/photo → ~50 hours for 30K. Per-shard fanout cuts this proportionally. |
| Single HNSW over 30K | Works, but per-shard indexes give faster incremental re-indexing and bound the noise floor per-query |
| Path-as-key fragility | `photos.id = name` today — file moves/renames break references that propagate through chunks and classified |
| Long-tail of natural-language filters | Vector + verify already handles enum-shaped queries (`indoor`, `from a plane`) because `BuildDocument` writes the canonical enums into the indexed text. What it can't do is range / numerical filters (`f/2.8 or wider`, `April 2024`) — those need structured filters. Open question whether the LLM-parse approach (Phase 7) earns its keep against explicit UI controls (date range picker, aperture slider) |

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
              │  • verify_cache (query × photo_id × model → verdict)  ← Now
              └──────────────┬───────────────┘
                  ┌──────────┼──────────┐
                  ↓          ↓          ↓
              x100vi       xt5         a7      ← Phase 5: per-camera schemas for chunks
              .chunks    .chunks    .chunks      cross-shard query = UNION ALL
                  └──────────┼──────────┘
                             ↓
              ┌──────────────────────────────┐
              │  LLM verify (8-way parallel) │  ← cached in verify_cache (Now)
              └──────────────────────────────┘
```

### Six design pillars

`When` column reads "Now" if the implementation runs in tree today, or `Phase N` if it ships in a future phase.

| Pillar | What it solves | Implementation | When |
|--------|----------------|----------------|------|
| 1. Bounded-complexity vector indexes | Recall stability + per-shard re-index cost | Per-camera schemas keep each HNSW ~3-10K rows; cross-camera search via `UNION ALL` | Phase 5 |
| 2. Incremental re-indexing | Wall-clock cost of adding photos | `cmd/index` skip-if-exists by default; `-reindex` truncates and rebuilds | Now (per-photo); Phase 5 (per-shard) |
| 3. Forced parallel workloads | Throughput before LLM is involved | `library.VerifyConcurrency = 8`; `UNION ALL` across shards compounds the parallelism | Now (verify); Phase 5 (shards) |
| 4. Cache layer | Repeat-query cost | `verify_cache(query, photo_id, verify_model, verdict, verified_at)`; freshness check via `verified_at > inference.described_at` invalidates cached verdicts when a photo is re-described | Now |
| 5. Structured filters from prose | Range / numerical predicates vector can't do (`f/2.8 or wider`, `April 2024`) | Optional LLM-parse step emits `{filters, residual}` against a json_schema sourced from `AllowedScalar`/`AllowedArray`; results cache-keyed off the same query string the verify cache uses | Phase 7 |
| 6. Single store for everything | Cache, portable moves, deep analysis | One Postgres DB holds all photo data including BLOB thumbnails; `pg_dump`/`pg_restore` (`scripts/db_dump.sh`, `scripts/db_restore.sh`) is the multi-machine sync workflow | Now |

---

## Key design decisions

These need to be pinned down before each relevant phase. Flagged with the phase that depends on them.

### A. Stable photo identity (Phase 6)

`file_path` is the de-facto key today. Files move → references through chunks/classified/photos break. Need a path-independent ID. The `photos.id` column already exists for this — Phase 6 populates it with a stable hash; until then it's the same string as `name`.

| Choice | Pros | Cons |
|--------|------|------|
| **SHA256 of file body** | True content identity; survives moves *and* renames; enables dedup detection | One-time hash cost (~minutes for 30K); changes if photo is re-edited |
| **Composite of EXIF date + filename + size** | Cheap; survives moves; survives reprocessing; doesn't read file body | Changes if EXIF is edited (rare); two distinct photos with identical EXIF+filename+size collide (vanishingly rare) |
| **UUID generated at describe time** | Cheap; stable through edits | Doesn't survive re-describe unless we propagate; no natural dedup |

**Default recommendation**: composite hash of `(exif_date_time_original, original_filename, file_size)`. Cheap, stable for the 99% case, no body read. Falls back to file body SHA256 if any composite component is missing.

### B. Shard key (Phase 5)

| Choice | Shard count | Pros | Cons |
|--------|-------------|------|------|
| `camera_model` (X100VI / X-T5 / A7…) | ~4-8 | Natural; aligns with how user thinks; fast camera-explicit routing | Imbalanced if one camera dominates |
| `make` (Fujifilm / Sony / DJI) | ~3-5 | Coarser; less imbalanced if user has multiple bodies of same brand | Loses model-level routing |
| `year` | ~5-10 | Time-balanced if shooting volume is roughly steady | Loses camera-explicit routing |
| Hybrid (`make+year`) | ~10-30 | Both axes bounded | Operational overhead grows with shard count |

**Default recommendation**: `camera_model` for the first cut. Re-evaluate if X100VI shard alone exceeds ~10K photos.

### C. Structured-filter routing (Phase 7)

The hand-rolled query-token parser approach was considered and dropped: every classify enum already lands in the embedded text via `library.BuildDocument`, so vector retrieval already prioritizes "indoor" / "from_plane" / etc. queries without a separate WHERE clause. The remaining filter cases are the ones vector can't do — numerical ranges and exact year/month — which is a much narrower problem.

| Choice | Pros | Cons |
|--------|------|------|
| Hand-rolled parser (regex + alias map) | Deterministic; cheap | Bespoke; rots as classify vocab evolves; reinvents NL parsing |
| LLM-parse with json_schema strict-mode (reuses `AllowedScalar`/`AllowedArray`) | Schema-driven — new classify columns auto-flow with no parser changes; same primitive `cmd/classify` already runs | One extra LLM round-trip per query; cacheable but adds latency on cold queries |
| Explicit UI controls (date range picker, aperture slider) | Predictable; no LLM dependency for filtering; works offline | Loses natural-language feel — users have to switch modes between typing and clicking |

**Default recommendation**: defer until evidence. Vector + verify already covers most queries; the cases that would benefit from structured filters are edge cases (specific aperture ranges, narrow date windows). When evidence appears, prefer LLM-parse over hand-rolled parser. Cache the parsed `{filters, residual}` against the canonical query — the verify-cache infra already does this for verdicts and the same key shape applies.

---

## What's left

Each phase delivers value independently. No phase is gated on a later one, but they compose.

| Phase | Delivers | Effort |
|-------|----------|--------|
| **5. Camera-sharded chunks** | `x100vi.chunks`, `xt5.chunks`, …; cross-shard search = `UNION ALL`; per-shard re-index = `TRUNCATE shard.chunks; INSERT …` | small |
| **6. Stable photo IDs** | Composite hash on `photos.id`; move/rename safe; dedup detection | medium |
| **7. Structured filters from prose** | LLM-parse the query against a json_schema sourced from `AllowedScalar`/`AllowedArray`; emit `{filters, residual}`; cache parses against the canonical query (same key shape as verify cache) | small (parse) + small (UI chips) |
| **8. Cross-shard analysis tooling** | `cmd/analyze` subcommands — lens diversity, aperture distribution, time-of-day heatmap, classifier vocabulary | small |

Highest near-term leverage: **Phase 5** — wall-clock cost of incremental re-indexing dominates everything else once the corpus crosses ~10K photos. Phase 7 is gated on evidence that vector + verify isn't enough; defer until the hit-rate UI surfaces a real recall gap.

---

## Phase notes (sketches, not specs)

### Phase 5: Camera-sharded chunks

- `chunks` moves from `public` schema into per-camera schemas: `x100vi.chunks`, `xt5.chunks`, `z8.chunks`, …
- New `public.shards (name, schema, last_indexed_at, photo_count)` registry
- Cross-shard query: `SELECT … FROM x100vi.chunks UNION ALL SELECT … FROM xt5.chunks ORDER BY similarity DESC LIMIT k`
- Per-shard re-index: `TRUNCATE x100vi.chunks; INSERT …` — bounded to one camera's data; doesn't disturb the other shards

The `photos` / `exif` / `descriptions` / `classified` / `inference` / `thumbnails` / `verify_cache` tables stay in `public` — they're the photo registry and cache, not shard data. Only `chunks` shards because vectors are the only table where index rebuild dominates wall-clock.

### Phase 6: Stable photo IDs

- Compute composite hash on next describe run (cheap — just EXIF + name + size)
- Migration: walk existing photos, populate `id` from composite, leaving `name` as the human-readable handle
- `chunks` / `classified` / `inference` / `thumbnails` / `verify_cache` keep their `photo_id` FK semantics; if `id` is the FK and `name` is just the display string, the FK is stable across renames and moves
- No re-embed required — embeddings keyed on `id`

### Phase 7: Structured filters via LLM-parse

Only justified once vector + verify is shown to miss on real queries (e.g. "f/2.8 or wider", "April 2024 only"). Sketch:

1. Build a json_schema from `AllowedScalar` / `AllowedArray` plus a sibling `exif` block with year, month, f-number range fields. Same construction `cmd/classify` already does — reuse the helper.
2. Single `LLMCompleteSchema` call per query → `{filters: {...}, residual: "..."}`
3. Cache the parse output against `(canonical_query)` in a sibling table `query_parse_cache` (same shape as `verify_cache`, just keyed on query alone). The verify cache's freshness primitive (`verified_at > inference.described_at`) doesn't apply here — query parses are independent of any photo's state, so the cache key is just the canonical query string.
4. WHERE predicates plug into the same SQL the existing search uses; residual is the embed query
5. Removable chip UI in `cmd/web` — "Filtering: f/≤2.8 · 2024" with `×` to drop a chip

### Phase 8: Cross-shard analysis tooling

`cmd/analyze` (or root-module package) with subcommands:

- `lens-stats` — most-used lenses by year (`exif.lens_model` × `exif.date_taken_year`)
- `aperture-distribution` — histogram per camera (`exif.f_number` GROUP BY)
- `coverage-by-camera-month` — heatmap data (cross-tabulate)
- `pov-breakdown` — `classified.pov_container` distribution per camera (was the X100VI mostly handheld? was the Z8 mostly aerial?)
- `vocabulary` — most common tokens in the FTS index for prose-feel auditing
- `classifier-disagreement` — photos where classifier output looks suspicious (flag for re-classify with stronger model)

Outputs Markdown tables / JSON; consumed by future "library dashboard" web view.
