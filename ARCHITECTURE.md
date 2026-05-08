# Architecture

The shape of the system today, plus what's still on the roadmap.

This is a living document — update when a phase ships or a decision changes.

---

## Current state (snapshot)

Four-stage Go pipeline against a single Postgres database with pgvector. The vector lane splits across three parallel halfvec(2560) stores; the search path adds three optional LLM steps:

```
photos on disk
    ↓
[1] cmd/describe      vision LLM → photos / exif / descriptions (incl. mood) /
                                    inference / thumbnails / query_generations
                                    (combined call emits Subject/.../Mood + Queries[])
    ↓
[2] cmd/classify      text LLM   → classified (typed enum fields from prose)
    ↓
[3] cmd/index         embedder   → photo_descriptions  (chunked scene + classifier prose)
                                   photo_metadata      (EXIF tokens, one row/photo)
                                   photo_queries       (LLM-generated phrasings, N rows/photo)
    ↓
[4] cmd/web           search UI  → six modes × per-store toggles × merge strategy
    cmd/search        CLI        × optional classifier filter × optional sort

Search-time pipeline (cmd/web):
   (auto-mode only)  rewrite NL → boolean       [LLM, ~250 tokens, query_rewrite_cache opt-in]
                                ↓
                     retrieve   (vector lane = SearchV2 across enabled stores,
                                  merged union/intersect/weighted; optionally
                                  fused with FTS via RRF in fts-vector modes)
                                ↓
   (toggle)          classifier filter           [LLM batched, classify_filter_cache opt-in]
                                ↓
   (verify modes)    prose verify per-candidate  [LLM 8-way pool, verify_cache always-on,
                                                  text composition mirrors UseDescriptions
                                                  + UseMetadata toggles; queries always
                                                  excluded from verifier text]
                                ↓
                     results
```

Six search modes: `vector`, `vector+verify`, `FTS+vector`, `FTS+vector+verify`, `auto`, `auto+verify`. Three orthogonal toggles compose with any mode: classifier filter (`?class=1`), per-store enable (`descriptions=1` / `metadata=1` / `queries=1`, all default on), merge strategy (`merge=union|intersect|weighted` with optional per-store weights `wd` / `wm` / `wq` under `weighted`).

All `cmd/*` binaries plus the `library/` package run against one DSN (`LIBRARY_DSN`, default `postgres:///ragotogar`). Per-stage HTTP endpoints (`VISION_ENDPOINT`, `TEXT_ENDPOINT`, `EMBED_ENDPOINT`) let each pillar hit a different LLM provider; `LM_STUDIO_BASE` is the legacy shared fallback. All LLM calls go through `library/http.go`'s retry+backoff layer (5 attempts, exponential jitter, honors `Retry-After`, ctx-cancel aware). Prompt templates live in `prompts/` and are embedded into binaries via `//go:embed` so there's a single source of truth.

Schema is Go-const + idempotent migrations (`migrate()` / `migrateV4..V13()`) applied at process start by `cmd/describe` (the schema authority). Other binaries open the DB and assume tables exist. Three LLM-result caches all use the same shape (canonical query as PK component, model in PK, freshness via `*_at > source_at`):

- `verify_cache(query, photo_id, verify_model, …)` — prose verify verdicts, freshness vs `inference.described_at`. Always-on.
- `query_rewrite_cache(nl_query, rewrite_model, …)` — auto-mode NL→boolean rewrites, no per-photo dependency. Opt-in via `?save=1` so iterating to a good rewrite isn't sticky.
- `classify_filter_cache(nl_query, photo_id, classify_model, …)` — post-retrieval drop verdicts, freshness vs `classified.classified_at`. Opt-in via `?save_class=1`.

FTS uses `websearch_to_tsquery` so the search box accepts boolean operators (phrase binding, OR, leading-`-` negation). Negation reaches both arms: FTS natively, vector via `library.StripNegation` on the embed input plus a post-filter `library.ExtractNegation` against `descriptions.fts || exif.fts`.

---

## Why this changes at scale

At 30K+ photos:

| Pain | Magnitude |
|------|-----------|
| Re-describe wall-clock | Single-stream Qwen3-VL 8B ≈ 6s/photo → ~50 hours for 30K. Per-shard fanout cuts this proportionally. |
| Single HNSW over 30K | Works, but per-shard indexes give faster incremental re-indexing and bound the noise floor per-query |
| Path-as-key fragility | `photos.id = name` today — file moves/renames break references that propagate through `photo_descriptions` / `photo_metadata` / `photo_queries` / `classified` |
| Long-tail of natural-language filters | `auto` mode (Now) rewrites NL → websearch boolean via an LLM, so `red trucks no monochrome` becomes `"red truck" -monochrome -"black and white" -grayscale -desaturated`. v8's `exif.fts` lets FTS reach camera / lens / year / software / artist literals (`2024`, `X100VI`) via `descriptions.fts ‖ exif.fts`. The classifier filter (Now) drops candidates whose `classified.*` enums contradict the NL request, closing the prose-vs-verdict gap (e.g. a photo whose prose says "no taxiway visible" but classifier verdict is `subject_altitude=in_air`). Range / numerical filters (`f/2.8 or wider`, aperture bounds) still aren't reachable; Phase 7 is the open question for those |
| Short / shallow describer prose | The Subject field demands both nouns AND verbs (`single-engine propeller airplane in flight, climbing` rather than `airplane silhouette`) so action and state land in the prose alongside the object. Forward-only — old descriptions stay terse until `-force` re-describe. |

The remaining phases address each.

---

## Target architecture

```
              ┌──────────────────────────────┐
              │  Search UI (web / CLI)       │
              └──────────────┬───────────────┘
                             ↓
              ┌──────────────────────────────┐  ← (auto modes only)
              │  LLM rewrite NL → boolean    │     query_rewrite_cache (opt-in via ?save=1)
              └──────────────┬───────────────┘
                             ↓
              ┌──────────────────────────────┐
              │  Postgres + pgvector         │
              │  • photos / exif / inference / thumbnails
              │  • descriptions (prose + mood + generated tsvector)
              │  • classified (typed enums from cmd/classify)
              │  • query_generations (LLM phrasings JSONB, source of truth)
              │  • photo_descriptions  (halfvec(2560) HNSW — scene + classifier)
              │  • photo_metadata      (halfvec(2560) HNSW — EXIF tokens, 1 row/photo)
              │  • photo_queries       (halfvec(2560) HNSW — per phrasing)
              │  • verify_cache             ← Now (always-on)
              │  • query_rewrite_cache      ← Now (opt-in)
              │  • classify_filter_cache    ← Now (opt-in)
              └──────────────┬───────────────┘
                  ┌──────────┼──────────┐
                  ↓          ↓          ↓
              x100vi       xt5         a7      ← Phase 5: per-camera schemas for the three v12 stores
              {3 stores}  {3 stores}  {3 stores}  cross-shard query = UNION ALL per-store
                  └──────────┼──────────┘
                             ↓
              ┌──────────────────────────────┐  ← (toggle ?class=1)
              │  Classifier filter           │     batched LLM, structured input
              │  (NL × classifier verdicts)  │     classify_filter_cache (opt-in)
              └──────────────┬───────────────┘
                             ↓
              ┌──────────────────────────────┐  ← (verify modes only)
              │  LLM prose verify (8-way)    │     verify_cache
              └──────────────────────────────┘
```

### Ten design pillars

`When` column reads "Now" if the implementation runs in tree today, or `Phase N` if it ships in a future phase.

| Pillar | What it solves | Implementation | When |
|--------|----------------|----------------|------|
| 0. Vector-lane signal separation | "Where did this match come from?" — v1's single concatenated `BuildDocument` blob mixed scene prose + EXIF + classifier verdicts into one embedding, so weird matches were unattributable. v12 splits the lane three ways so each signal type carries its own embedding and can be toggled independently | `photo_descriptions` / `photo_metadata` / `photo_queries` halfvec(2560) HNSW stores. `cmd/index` populates each independently with per-store skip-if-exists keyed `(photo_id, schema_version)`. `cmd/search` / `cmd/web` expose `-use-descriptions / -use-metadata / -use-queries` toggles + `-merge-strategy=union/intersect/weighted`. Verifier text composition mirrors the enable toggles (queries always excluded — verifier never sees its own training-target text). See "v12 design decisions" below for the locked-in choices behind this pillar | Now |
| 1. Bounded-complexity vector indexes | Recall stability + per-shard re-index cost | Per-camera schemas keep each HNSW ~3-10K rows; cross-camera search via `UNION ALL` per store (compounds with Pillar 0) | Phase 5 |
| 2. Incremental re-indexing | Wall-clock cost of adding photos | `cmd/index` skip-if-exists by default per store; `-reindex=descriptions,metadata,queries` accepts subset list (replaces v11's global `-reindex` bool) | Now (per-photo, per-store); Phase 5 (per-shard) |
| 3. Forced parallel workloads | Throughput before LLM is involved | `library.VerifyConcurrency = 8`; `UNION ALL` across shards compounds the parallelism; `cmd/describe` skip-exists pass also fans out across `PREVIEW_WORKERS` so all-skip runs scale linearly with worker count instead of sequentially through `exiftool` | Now (verify, prep); Phase 5 (shards) |
| 4. Cache layer | Repeat-query cost; iteration without stickiness | Three caches, all (canonical_query, …, model) keyed: `verify_cache` (always-on, freshness vs `inference.described_at`); `query_rewrite_cache` (opt-in via `?save=1` so iterating to a good rewrite doesn't memorialize bad output); `classify_filter_cache` (opt-in via `?save_class=1`, freshness vs `classified.classified_at`) | Now |
| 5. Boolean operators in the FTS lane | Attribute binding and exclusion the embedder can't express on its own | `websearch_to_tsquery` parses the search box: `"red truck"` for adjacency, `OR`, leading-`-` negation. Negation reaches the vector arm too via `library.StripNegation` on the embed input + post-filter `library.ExtractNegation` against `descriptions.fts ‖ exif.fts` | Now |
| 6. LLM query rewriter | Bridges user NL vocabulary to describer prose vocabulary so users don't need to learn boolean syntax | `library.RewriteQuery` calls a small text model with the embedded `prompts/query.md` template; output is one boolean line. Composes with FTS+vector retrieval as `auto` / `auto+verify` modes. Cache opt-in (default off) | Now |
| 7. Classifier-aware post-retrieval filter | Prose-vs-verdict gap — when a photo's description contains a negated lexeme but its classifier enum says otherwise (or vice versa) | `library.FilterByClassification` sends one batched LLM call per query: NL request + each candidate's compact classifier row → strict `{drop_ids:[…]}` json_schema. Domain-agnostic semantic mapping (no static aliases). Cache opt-in | Now |
| 8. Structured filters from prose | Range / numerical predicates vector can't do (`f/2.8 or wider`, `April 2024`) | Optional LLM-parse step emits `{filters, residual}` against a json_schema sourced from `AllowedScalar`/`AllowedArray`; results cache-keyed off the same query string the verify cache uses | Phase 7 |
| 9. Single store for everything | Cache, portable moves, deep analysis | One Postgres DB holds all photo data including BLOB thumbnails; `pg_dump`/`pg_restore` (`scripts/db_dump.sh`, `scripts/db_restore.sh`) is the multi-machine sync workflow | Now |

---

## Key design decisions

These need to be pinned down before each relevant phase. Flagged with the phase that depends on them.

### v12 design decisions (Pillar 0)

The three-store split shipped in migrations v12 (tables) + v13 (`descriptions.mood` column). The locked choices that future store additions or prompt edits should respect:

| Decision | Resolution | Why |
|----------|------------|-----|
| Embedding dimension | `halfvec(2560)` | Native Qwen3-Embedding-4B output. No Matryoshka truncation — full dimension space and HNSW-viable (`vector` type's HNSW caps at 2000 dims, `halfvec` at 4000). |
| `photo_metadata` text format | Tokens, not natural-language sentence | Closest to the EXIF emit shape of v1's `BuildDocument`; preserves token-exact embedder signal for `X100VI`, `f/2.8`, etc. The FTS arm via `exif.fts` already catches literal-token queries; the metadata-store embedding is the dense-vector counterpart for prose-shaped queries (`"shot at fast shutter"`). |
| Verifier input | Mirrors retrieval toggles | If `UseMetadata` is on at search time, the verifier sees `BuildDescriptionDocument + BuildMetadataDocument`; otherwise it sees descriptions only. Queries are *always* excluded — verifier must never see its own training-target text. One knob instead of two parallel ones. |
| Query-gen pass timing | Combined single vision call | The describer's structured output emits `Subject` / `Setting` / `Light` / `Colors` / **`Mood`** / `Composition` / `Vantage` / `GroundTruth` / `Condition` / **`Queries[]`** in one request. No separate `cmd/queries` binary; backfill = `cmd/describe -force`. Risk to monitor: longer structured output amplifies truncation/malform under high-concurrency providers (OpenRouter); parser must log explicit malformed responses, never write empty/broken JSON silently. |
| Query-gen output storage | `query_generations` DB table (JSONB) | No `photos/queries/*.json` sidecars — contradicts the existing single-DB-as-source-of-truth posture. The table holds raw phrasings + `prompt_hash` + `model`; `photo_queries` is the embedded form derived from it. |
| Per-store `schema_version` semantics | Stamped per-row, starts at 2 | Lets a prompt change in one stage (e.g. re-running query gen against a new model) bump that store's version without forcing re-embed of the others. Global migration counter (`schema_version` table) is independent — runs v4..v14. |
| FTS arm | Unchanged | `descriptions.fts ‖ exif.fts` stays where it is; only the *vector* lane splits. `descriptions.fts` includes `mood` per the v13 fts rebuild. |

### A. Stable photo identity (Phase 6)

`file_path` is the de-facto key today. Files move → references through `photo_descriptions` / `photo_metadata` / `photo_queries` / `classified` break. Need a path-independent ID. The `photos.id` column already exists for this — Phase 6 populates it with a stable hash; until then it's the same string as `name`.

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

The hand-rolled query-token parser approach was considered and dropped: every classify enum already lands in the embedded text via `library.BuildDescriptionDocument`, so vector retrieval already prioritizes "indoor" / "from_plane" / etc. queries without a separate WHERE clause. The remaining filter cases are the ones vector can't do — numerical ranges and exact year/month — which is a much narrower problem.

| Choice | Pros | Cons |
|--------|------|------|
| Hand-rolled parser (regex + alias map) | Deterministic; cheap | Bespoke; rots as classify vocab evolves; reinvents NL parsing |
| LLM-parse with json_schema strict-mode (reuses `AllowedScalar`/`AllowedArray`) | Schema-driven — new classify columns auto-flow with no parser changes; same primitive `cmd/classify` already runs | One extra LLM round-trip per query; cacheable but adds latency on cold queries |
| Explicit UI controls (date range picker, aperture slider) | Predictable; no LLM dependency for filtering; works offline | Loses natural-language feel — users have to switch modes between typing and clicking |

**Default recommendation**: defer until evidence. Vector + verify already covers most queries; the cases that would benefit from structured filters are edge cases (specific aperture ranges, narrow date windows). When evidence appears, prefer LLM-parse over hand-rolled parser. Cache the parsed `{filters, residual}` against the canonical query — the verify-cache infra already does this for verdicts and the same key shape applies.

**Evidence on file (priority lift):**

- *Camera-anchored queries leak in hybrid mode.* Validation query `planes on taxiways ... with a nikon z8` (auto + FTS+vector, ~3.2k photos) returned 14 matches, none actually shot on a Z8. Two tokenization / fusion failures compound:
  1. EXIF stores `NIKON Z 8` (with space); `to_tsvector('english', …)` lexemes are `nikon`, `z`, `8`. The query token `z8` doesn't stem-match any of those individually, so the FTS arm returns zero rows.
  2. The vector arm treats `nikon z8` as soft semantic context, which gets drowned by the dominant scene signal ("planes on taxiways"). RRF doesn't enforce intersection across arms — it unions ranked lists, so vector matches survive even when FTS contributed nothing.
- Phase 7's LLM-parse is the right fix: rewrite `with a nikon z8` to `{filters: {camera_model: "NIKON Z 8"}, residual: "planes on taxiways"}`, AND the filter onto candidates as a SQL predicate. Same canonical-query cache shape as `query_rewrite_cache`.
- Workaround until Phase 7 ships: `vector+verify` (the verifier sees full description + metadata text and correctly drops non-Z8 photos), or smaller scope hot-fix — a `RequirePositive(query)` post-filter that hard-enforces only tokens also present in the EXIF FTS vocabulary (camera_make / camera_model / lens_model / year / software / artist), leaving scene words soft. ~30 lines.

---

## What's left

Each phase delivers value independently. No phase is gated on a later one, but they compose.

| Phase | Delivers | Effort |
|-------|----------|--------|
| **5. Camera-sharded v12 stores** | `x100vi.{photo_descriptions,photo_metadata,photo_queries}`, `xt5.{...}`, …; cross-shard search = `UNION ALL` per store; per-shard re-index scoped to one camera's data. | small-medium |
| **6. Stable photo IDs** | Composite hash on `photos.id`; move/rename safe; dedup detection | medium |
| **7. Structured filters extension to auto rewrite** | Extend the existing `prompts/query.md` LLM-parse step to also emit `{filters: {...}}` against a json_schema sourced from `AllowedScalar`/`AllowedArray`; reuse `query_rewrite_cache` keying | small (parse) + small (UI chips) |
| **8. Cross-shard analysis tooling** | `cmd/analyze` subcommands — lens diversity, aperture distribution, time-of-day heatmap, classifier vocabulary | small |

Highest near-term leverage: **Phase 5** — wall-clock cost of incremental re-indexing dominates everything else once the corpus crosses ~10K photos. Phase 7 is gated on evidence that the auto rewrite + classifier filter combo still misses real queries; defer until the in-UI stats surface a real recall gap.

---

## Phase notes (sketches, not specs)

### Phase 5: Camera-sharded v12 stores

- The three v12 vector stores move from `public` into per-camera schemas: `x100vi.{photo_descriptions, photo_metadata, photo_queries}`, `xt5.{…}`, `z8.{…}`, …
- New `public.shards (name, schema, last_indexed_at, photo_count)` registry
- Cross-shard query: `SELECT … FROM x100vi.photo_descriptions UNION ALL SELECT … FROM xt5.photo_descriptions ORDER BY similarity DESC LIMIT k` (per store; SearchV2's per-store fan-out becomes per-shard × per-store)
- Per-shard re-index: scoped to one camera's data; doesn't disturb other shards

The `photos` / `exif` / `descriptions` / `classified` / `inference` / `thumbnails` / `query_generations` / `verify_cache` / `query_rewrite_cache` / `classify_filter_cache` tables stay in `public` — they're the photo registry, source-of-truth, and caches. Only the three vector stores shard because they're the only tables where index rebuild dominates wall-clock.

### Phase 6: Stable photo IDs

- Compute composite hash on next describe run (cheap — just EXIF + name + size)
- Migration: walk existing photos, populate `id` from composite, leaving `name` as the human-readable handle
- `photo_descriptions` / `photo_metadata` / `photo_queries` / `query_generations` / `classified` / `inference` / `thumbnails` / `verify_cache` keep their `photo_id` FK semantics; if `id` is the FK and `name` is just the display string, the FK is stable across renames and moves
- No re-embed required — embeddings keyed on `id`

### Phase 7: Structured filters via LLM-parse

Pillar 6 (`auto` mode) already ships the LLM-parse primitive — it produces `{rewritten: "boolean string"}`. Phase 7 extends the same primitive to also emit numerical / range filters that pgvector + FTS can't express. Sketch:

1. Build a json_schema from `AllowedScalar` / `AllowedArray` plus a sibling `exif` block with year, month, f-number range fields. Same construction `cmd/classify` already does — reuse the helper.
2. Single `LLMCompleteSchema` call per query → `{boolean: "...", filters: {...}}`. Replaces or extends the current `prompts/query.md` rewrite output.
3. Cache the parse output against `(canonical_query, rewrite_model)` — same shape as the existing `query_rewrite_cache`, possibly the same table with an additional column. Opt-in like the rewrite cache.
4. WHERE predicates plug into the same SQL the existing search uses; boolean field is the existing rewrite output
5. Removable chip UI in `cmd/web` — "Filtering: f/≤2.8 · 2024" with `×` to drop a chip

The rewrite cache table (already in v10) has the right shape — extension is incremental. Gated on evidence that vector + verify + classifier filter still misses real queries; defer until the in-UI stats surface a real recall gap.

### Phase 8: Cross-shard analysis tooling

`cmd/analyze` (or root-module package) with subcommands:

- `lens-stats` — most-used lenses by year (`exif.lens_model` × `exif.date_taken_year`)
- `aperture-distribution` — histogram per camera (`exif.f_number` GROUP BY)
- `coverage-by-camera-month` — heatmap data (cross-tabulate)
- `pov-breakdown` — `classified.pov_container` distribution per camera (was the X100VI mostly handheld? was the Z8 mostly aerial?)
- `vocabulary` — most common tokens in the FTS index for prose-feel auditing
- `classifier-disagreement` — photos where classifier output looks suspicious (flag for re-classify with stronger model)

Outputs Markdown tables / JSON; consumed by future "library dashboard" web view.
