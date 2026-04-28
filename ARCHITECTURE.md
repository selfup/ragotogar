# Architecture

The target shape of ragotogar at 30K+ photos, plus the phased rollout to get there.

This is a living document — update when a phase ships or a decision changes.

---

## Why this changes at scale

At 30K+ photos:

| Pain | Magnitude |
|------|-----------|
| Full re-index time | 25–30 hours (Ministral 3B at 5s/photo, ~3 parallel workers) |
| Structured filter ("April + f/2 + Fuji") | Requires walking the whole library each query |
| Repeat queries | Pay full retrieval + verify cost every time |
| Path-as-key fragility | Files move → indexes desync → silent breakage |
| Single-graph entity density | Cross-camera connections dilute per-camera entity coherence |
| Memory at full scale | Single LightRAG index with 30K vectors + entity graph fits, but headroom shrinks |

The architecture below addresses each.

---

## Target architecture

```
              ┌────────────────────────────┐
              │  Search UI (web / CLI)     │
              └──────────────┬─────────────┘
                             ↓
              ┌────────────────────────────┐
              │  SQL frontend (SQLite)     │
              │  • metadata index          │
              │  • query+verify cache      │
              │  • photo_id ↔ path map     │
              │  • shard routing           │
              └──────────────┬─────────────┘
                  ┌──────────┼──────────┐
                  ↓          ↓          ↓
              X100VI       X-T5        A7      ← per-camera GraphRAG shards
              GraphRAG    GraphRAG   GraphRAG    parallel fan-out
                  └──────────┼──────────┘
                             ↓
              ┌────────────────────────────┐
              │  LLM verify (Ministral 8x) │  ← cached at SQL layer
              └────────────────────────────┘
```

### Six design pillars

| Pillar | What it solves | Implementation |
|--------|----------------|----------------|
| 1. Bounded-complexity graphs | Search query optimization at scale | Camera-sharded LightRAG indexes (~3-10K docs each) keep entity density high without combinatorial blowup |
| 2. Incremental re-indexing | Wall-clock cost of adding photos | Each shard re-indexes independently; adding a month of X100VI ≈ 30 min, not 30 hrs |
| 3. Forced parallel workloads | Throughput before LLM is involved | Fan-out across N shards = N× retrieval parallelism; verify is already 8-way; compounds |
| 4. Cache layer | Repeat-query cost | SQL-backed: `(query, photo_id) → verdict` for verify, `(query, mode, shard) → file_paths` for retrieval; invalidated on shard re-index timestamp |
| 5. SQL frontend | Structured filters + cache lookup | SQLite over EXIF metadata; pre-filter narrows candidate set before any retrieval runs |
| 6. SQL is also the data store | Cache, portable moves, deep analysis | Photos table with stable IDs; SQL aggregations enable "lens diversity by year"-shaped questions; immutable moves work because IDs survive path changes |

---

## Key design decisions

These need to be pinned down before each relevant phase. Not all need answers now — flagged with the phase that depends on them.

### A. Stable photo identity (Phase 5)

`file_path` is the de-facto key today (LightRAG, web URLs). Files move → indexes break. Need a path-independent ID. The `photos.id` column already exists for this — Phase 5 populates it with a stable hash; until then it's the same string as `name`.

| Choice | Pros | Cons |
|--------|------|------|
| **SHA256 of file body** | True content identity; survives moves *and* renames; enables dedup detection | One-time hash cost (~minutes for 30K); changes if photo is re-edited |
| **Composite of EXIF date + filename + size** | Cheap; survives moves; survives reprocessing; doesn't read file body | Changes if EXIF is edited (rare); two distinct photos with identical EXIF+filename+size collide (vanishingly rare) |
| **UUID generated at describe time** | Cheap; stable through edits | Doesn't survive re-describe unless we propagate; no natural dedup |

**Default recommendation**: composite hash of `(exif_date_time_original, original_filename, file_size)`. Cheap, stable for the 99% case, no body read needed. Falls back to file body SHA256 if any composite component is missing.

### B. Shard key (Phase 4)

| Choice | Shard count | Pros | Cons |
|--------|-------------|------|------|
| `camera_model` (X100VI / X-T5 / A7…) | ~4-8 | Natural; aligns with how user thinks; fast camera-explicit routing | Imbalanced if one camera dominates (e.g. 25K X100VI + 500 each other) |
| `make` (Fujifilm / Sony / DJI) | ~3-5 | Coarser; fewer shards; less imbalanced if user has multiple bodies of same brand | Loses model-level routing |
| `year` | ~5-10 | Time-balanced if shooting volume is roughly steady | Loses camera-explicit routing; rebalances naturally with new years |
| Hybrid (`make+year`) | ~10-30 | Both axes bounded | Operational overhead grows with shard count |

**Default recommendation**: `camera_model` for the first cut. Re-evaluate if X100VI shard alone exceeds ~10K photos.

### C. Cache invalidation granularity (Phase 3)

| Choice | Pros | Cons |
|--------|------|------|
| Per-shard `last_indexed_at` invalidates all `(query, shard)` entries | Simple; matches re-index workflow | Coarse — adding one photo invalidates the whole shard's cache |
| Per-photo `(photo_id, query)` with photo timestamp | Surgical | More state, more complexity |

**Default recommendation**: per-shard. Matches the re-indexing model from pillar 2.

---

## Phased rollout

Each phase delivers value independently. No phase is gated on a later one, but they compose.

| Phase | Delivers | Effort |
|-------|----------|--------|
| **2. SQL pre-filter in cmd/web search path** | "SQL filter → vector → verify" pipeline end-to-end | small |
| **3. Verify-result cache in SQL** | Repeat queries become free | small |
| **4. Camera-sharded LightRAG indexes + super-graph router** | Bounded shards, parallel fan-out, incremental re-indexing | medium |
| **5. Stable photo IDs + path-portability migration** | Move/rename safe; dedup detection | medium |
| **6. Cross-shard analysis tooling (SQL aggregations)** | "Most-used aperture in 2024", lens diversity reports, etc. | small |

Highest near-term leverage: Phase 2 (start using the SQL index in the search path).

---

## Phase notes (sketches, not specs)

These get fleshed out when their phase starts.

### Phase 2: SQL pre-filter in cmd/web

- `cmd/web` parses the query for structured tokens (camera names, year mentions, aperture-shaped substrings)
- Looks up photo_ids matching those structured filters via SQL
- Passes the candidate set to `search.py --restrict-to <names>` (search.py already reads description text from SQL, so name-based restriction is straightforward)

Open: how to pass a candidate set to LightRAG cleanly. May need a wrapper that runs vector search itself instead of going through search.sh.

### Phase 3: Verify cache

```sql
CREATE TABLE verify_cache (
    query        TEXT NOT NULL,
    photo_id     TEXT NOT NULL REFERENCES photos(id),
    verdict      INTEGER NOT NULL,           -- 0/1
    verified_at  TEXT NOT NULL,
    shard_indexed_at TEXT,                   -- last_indexed_at of the shard at verify time
    PRIMARY KEY (query, photo_id)
);
```

Lookup: `(query, photo_id)` and `verified_at > shards.last_indexed_at[shard_of_photo]`. If hit, skip LLM call.

### Phase 4: Camera-sharded LightRAG

- New table `shards (name, index_dir, last_indexed_at, photo_count)`
- Each shard is its own `tools/.rag_index_<camera>/` directory
- `index_and_vectorize.py` learns `--shard <name>` to write into the right dir
- `search.py` fans out across all shards by default, dedupes results by photo_id
- Super-graph router: start with always-fan-out; add keyword router if the wasted cost becomes meaningful

### Phase 5: Stable photo IDs

- Compute composite hash on next describe run (cheap — just EXIF + name + size)
- Migration: walk existing photos, populate `id` column from composite, leaving `name` as the human-readable handle
- LightRAG re-index needed; gives us the path-portability invariant

### Phase 6: Analysis tooling

- `tools/analyze.py` with subcommands:
  - `lens-stats` — most-used lenses by year
  - `aperture-distribution` — histogram
  - `coverage-by-camera-month` — heatmap data
  - `entity-frequency` — which graph entities recur most
- Outputs JSON or Markdown tables; consumed by future "library dashboard" web view
