# Architecture

The target shape of ragotogar at 30K+ photos, plus the phased rollout to get there from today's flat-file pipeline. Phase 1 is spec'd in detail at the end; later phases are design-level.

This is a living document — update when a phase ships or a decision changes.

---

## Why this changes at scale

Today (sub-1K photos): JSON files + LightRAG vector store + cashier-rendered MD/HTML/JPG sidecars. All file-based. Works great.

At 30K+ photos:

| Pain | Magnitude |
|------|-----------|
| Full re-index time | 25–30 hours (Ministral 3B at 5s/photo, ~3 parallel workers) |
| Structured filter ("April + f/2 + Fuji") | Requires walking 30K JSONs each query |
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

### A. Stable photo identity (Phase 5, but Phase 1 schema should accommodate)

`file_path` is the de-facto key today (LightRAG, cashier outputs, web URLs). Files move → indexes break. Need a path-independent ID.

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

### C. SQLite FTS5 (Phase 1 or 2)

SQLite has FTS5, which gives keyword search on descriptions for free. Could complement vector search:

- **FTS5**: high precision on exact terms ("Walmart", "DSCF1611", literal text in description)
- **Vector**: high recall on semantic concepts ("car interiors", "warm light")

| Add FTS5 in Phase 1? | Skip until later? |
|----------------------|-------------------|
| Negligible storage cost; one extra index | Phase 1 stays focused on metadata filter |
| Enables literal/exact-text queries that vector struggles with | Add when a concrete need surfaces |

**Default recommendation**: include FTS5 in Phase 1. The cost is one virtual table; the upside is a third retrieval lane (literal text) we don't currently have.

### D. Cache invalidation granularity (Phase 3)

| Choice | Pros | Cons |
|--------|------|------|
| Per-shard `last_indexed_at` invalidates all `(query, shard)` entries | Simple; matches re-index workflow | Coarse — adding one photo invalidates the whole shard's cache |
| Per-photo `(photo_id, query)` with photo timestamp | Surgical | More state, more complexity |

**Default recommendation**: per-shard. Matches the re-indexing model from pillar 2.

### E. SQL write strategy (Phase 1)

| Choice | Pros | Cons |
|--------|------|------|
| **Write-through** from cmd/describe | Always-fresh; one fewer step | Couples describe to SQL; cmd/describe is Go, SQLite client adds dep |
| **Standalone sync tool** | Decoupled; idempotent; can be re-run | Two-step workflow (describe → sql_sync) |
| **Hybrid**: cashier writes SQL row when it writes outputs | Cashier already touches the JSON; one place | Couples cashier to SQL |

**Default recommendation**: standalone sync tool for Phase 1 (`tools/sql_sync.py`). Easiest to reason about, idempotent, can be hooked into `dir_photos.sh` as a final step. Re-evaluate if the indirection becomes a friction point.

---

## Phased rollout

Each phase delivers value independently. No phase is gated on a later one, but they compose.

| Phase | Delivers | Dependencies | Effort |
|-------|----------|--------------|--------|
| **1. SQLite metadata index** | Fast structured filters; foundation for caching, sharding, deep analysis | none | small |
| **2. SQL pre-filter in cmd/web search path** | "SQL filter → vector → verify" pipeline end-to-end | Phase 1 | small |
| **3. Verify-result cache in SQL** | Repeat queries become free | Phase 1 | small |
| **4. Camera-sharded LightRAG indexes + super-graph router** | Bounded shards, parallel fan-out, incremental re-indexing | Phase 1 (for shard registry) | medium |
| **5. Stable photo IDs + path-portability migration** | Move/rename safe; dedup detection | Phase 1 | medium |
| **6. Cross-shard analysis tooling (SQL aggregations)** | "Most-used aperture in 2024", lens diversity reports, etc. | Phase 1 | small once SQL is in |

Highest near-term leverage: Phase 1. Everything else builds on top.

---

# Phase 1: SQLite metadata index — detailed spec

## Goal

Provide a structured-query layer over the existing JSON files. Fast filters, aggregations, and the foundation for caching, sharding, and stable IDs.

## Non-goals (this phase)

- **Not** replacing JSON as the source of truth — JSONs stay authoritative; SQLite is a derived index
- **Not** implementing the cache layer — Phase 3
- **Not** sharding the LightRAG index — Phase 4
- **Not** stable-ID migration — Phase 5 (but schema reserves the column)

## Schema

Stored at `tools/.sql_index/library.db` (gitignored, parallel to `tools/.rag_index/`).

```sql
-- Schema version for forward-compatible migrations.
CREATE TABLE IF NOT EXISTS schema_version (
    version INTEGER PRIMARY KEY,
    applied_at TEXT NOT NULL
);

-- Photo identity. id is reserved for stable hash/UUID (Phase 5);
-- in Phase 1 we populate id = name to keep things simple.
CREATE TABLE IF NOT EXISTS photos (
    id            TEXT PRIMARY KEY,           -- Phase 1: same as name; Phase 5: composite hash
    name          TEXT NOT NULL UNIQUE,       -- e.g. 20240421_X100VI_DSCF0085
    json_path     TEXT NOT NULL,              -- absolute path to .json sidecar
    file_path     TEXT,                       -- absolute path to original photo (data.path)
    md_path       TEXT,                       -- cashier .md if present
    html_path     TEXT,                       -- cashier .html if present
    jpg_path      TEXT,                       -- cashier .jpg sidecar if present
    file_basename TEXT,                       -- e.g. DSCF0085.JPG
    created_at    TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at    TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX IF NOT EXISTS idx_photos_name ON photos(name);

-- Denormalized EXIF for fast structured filters. NULL when EXIF field missing.
CREATE TABLE IF NOT EXISTS exif (
    photo_id              TEXT PRIMARY KEY REFERENCES photos(id) ON DELETE CASCADE,
    camera_make           TEXT,
    camera_model          TEXT,
    lens_model            TEXT,
    lens_info             TEXT,
    date_taken            TEXT,                -- ISO 8601 derived from date_time_original
    date_taken_year       INTEGER,             -- denormalized for cheap year aggregations
    date_taken_month      INTEGER,             -- 1-12
    focal_length_mm       REAL,                -- parsed from "23.0 mm"
    focal_length_35mm     REAL,
    f_number              REAL,
    exposure_time_seconds REAL,                -- parsed from "1/250" → 0.004
    iso                   INTEGER,
    exposure_compensation REAL,                -- parsed from "-0.67" → -0.67 (EV)
    exposure_mode         TEXT,
    metering_mode         TEXT,
    white_balance         TEXT,
    flash                 TEXT,
    image_width           INTEGER,
    image_height          INTEGER,
    gps_latitude          REAL,
    gps_longitude         REAL,
    artist                TEXT,
    software              TEXT
);
CREATE INDEX IF NOT EXISTS idx_exif_camera         ON exif(camera_model);
CREATE INDEX IF NOT EXISTS idx_exif_make           ON exif(camera_make);
CREATE INDEX IF NOT EXISTS idx_exif_date           ON exif(date_taken);
CREATE INDEX IF NOT EXISTS idx_exif_year_month     ON exif(date_taken_year, date_taken_month);
CREATE INDEX IF NOT EXISTS idx_exif_iso            ON exif(iso);
CREATE INDEX IF NOT EXISTS idx_exif_aperture       ON exif(f_number);
CREATE INDEX IF NOT EXISTS idx_exif_focal          ON exif(focal_length_mm);
CREATE INDEX IF NOT EXISTS idx_exif_artist         ON exif(artist);

-- Visual fields broken out so individual filters work
-- ("photos with X in the subject field" without scanning full description).
CREATE TABLE IF NOT EXISTS descriptions (
    photo_id          TEXT PRIMARY KEY REFERENCES photos(id) ON DELETE CASCADE,
    subject           TEXT,
    setting           TEXT,
    light             TEXT,
    colors            TEXT,
    composition       TEXT,
    full_description  TEXT
);

-- Full-text search on the visual content. SQLite FTS5 virtual table.
-- Lets queries like "Walmart" or "DSCF1611" hit literal text without
-- relying on embedding similarity.
CREATE VIRTUAL TABLE IF NOT EXISTS descriptions_fts USING fts5(
    subject, setting, light, colors, composition, full_description,
    content=descriptions, content_rowid=rowid
);

-- Triggers keep FTS in sync with descriptions table.
CREATE TRIGGER IF NOT EXISTS descriptions_ai AFTER INSERT ON descriptions BEGIN
    INSERT INTO descriptions_fts(rowid, subject, setting, light, colors, composition, full_description)
    VALUES (new.rowid, new.subject, new.setting, new.light, new.colors, new.composition, new.full_description);
END;
CREATE TRIGGER IF NOT EXISTS descriptions_ad AFTER DELETE ON descriptions BEGIN
    INSERT INTO descriptions_fts(descriptions_fts, rowid, subject, setting, light, colors, composition, full_description)
    VALUES ('delete', old.rowid, old.subject, old.setting, old.light, old.colors, old.composition, old.full_description);
END;
CREATE TRIGGER IF NOT EXISTS descriptions_au AFTER UPDATE ON descriptions BEGIN
    INSERT INTO descriptions_fts(descriptions_fts, rowid, subject, setting, light, colors, composition, full_description)
    VALUES ('delete', old.rowid, old.subject, old.setting, old.light, old.colors, old.composition, old.full_description);
    INSERT INTO descriptions_fts(rowid, subject, setting, light, colors, composition, full_description)
    VALUES (new.rowid, new.subject, new.setting, new.light, new.colors, new.composition, new.full_description);
END;
```

## Sync strategy

Standalone tool: `tools/sql_sync.py`.

### Behavior

```
sql_sync.py <json_dir> [--db PATH] [--reset]

  Walks <json_dir> recursively for *.json. For each:
    • UPSERTs the photos row (keyed by name)
    • UPSERTs the exif row (parses EXIF strings → typed columns)
    • UPSERTs the descriptions row (splits the JSON's `fields` and `description`)
    • FTS triggers fire automatically

  Idempotent — re-running picks up new files and refreshes existing rows
  whose updated_at is older than the JSON's mtime.

  --reset drops and recreates all tables before sync.
  --db overrides the default tools/.sql_index/library.db path.
```

### EXIF parsing rules

| Source field (string) | Target column | Parse rule |
|-----------------------|---------------|------------|
| `"23.0 mm"` | `focal_length_mm REAL` | strip ` mm`, parse float; NULL on parse fail |
| `"f/2"` or `"2"` | `f_number REAL` | strip `f/`, parse float |
| `"1/250"` | `exposure_time_seconds REAL` | parse `num/denom` → float |
| `"3200"` | `iso INTEGER` | parse int |
| `"-0.67"` / `"0"` / `"-3"` | `exposure_compensation REAL` | parse float; always present in observed dataset (X100VI firmware) |
| `"2024:04:21 16:27:54"` | `date_taken TEXT (ISO 8601)` | parse → `2024-04-21T16:27:54`; also fill `date_taken_year`, `date_taken_month` |
| `"50.123"` (string) | `gps_latitude REAL` | parse float |
| Any field missing | column | NULL |

Parse failures log a warning and write NULL — don't fail the whole sync.

### Wiring into existing pipeline

`scripts/dir_photos.sh` gains a final step:

```bash
echo ""
echo "==> Syncing metadata to SQLite"
"$SCRIPT_DIR/sql_sync.sh" "$OUT_DIR"
```

Standalone use:

```bash
./tools/sql_sync.sh /path/to/describe_april
./tools/sql_sync.sh --reset /path/to/describe_april   # nuke and rebuild
```

## Querying

Phase 1 ships with raw SQL access only. No new search modes yet (those are Phase 2).

```bash
sqlite3 tools/.sql_index/library.db
> SELECT name, camera_model, date_taken FROM photos JOIN exif ON photos.id = exif.photo_id
  WHERE camera_model = 'X100VI' AND date_taken_year = 2024 LIMIT 20;
```

Convenience wrapper for ad-hoc queries:

```bash
./tools/sql_query.sh "SELECT camera_model, COUNT(*) FROM exif GROUP BY camera_model"
./tools/sql_query.sh -f /path/to/query.sql
```

## Tests

| Test | What it covers |
|------|----------------|
| `test_schema_init` | All tables and indexes create cleanly |
| `test_upsert_idempotency` | Running sync twice on the same JSON produces same row |
| `test_exif_parsing` | `"23.0 mm"` → 23.0, `"1/250"` → 0.004, `"f/2"` → 2.0, garbage → NULL with warning |
| `test_date_decomposition` | `"2024:04:21 16:27:54"` → year=2024, month=4, ISO date populated |
| `test_fts_search` | Insert a description, FTS query returns the row |
| `test_cascade_delete` | Deleting from photos removes exif + descriptions + FTS |
| `test_missing_fields_dont_fail` | JSON without `metadata.lens_model` writes NULL, no crash |

Goes into a new `tools/sql_sync_test.py` (use `pytest` since we already have `uv` managing deps; add `pytest` to `tools/pyproject.toml`).

## Migration

For users with existing describe_* dirs:

```bash
./tools/sql_sync.sh describe_april        # populates SQLite from existing JSONs
./tools/sql_sync.sh describe_test_p4
# repeat per dir; or point at the parent
./tools/sql_sync.sh ~/X100VI/JPEG/March/descriptions
```

Idempotent — safe to re-run.

## Open questions to resolve before code goes in

1. **Dependencies**: Python's stdlib `sqlite3` is enough for everything Phase 1 needs (no ORM required). Stick with stdlib? Or add `sqlmodel` / `sqlalchemy` for schema management ergonomics? **Default**: stdlib for now; revisit if multi-table joins in Python get clunky.
2. **Migration framework**: just a `schema_version` table with handwritten migrations, or pull in `alembic`? **Default**: handwritten; scope is small.
3. **Where to put it in the repo**: `tools/sql_sync.py` next to existing scripts? Or a new `cmd/sqlsync` Go program? **Default**: Python in `tools/` to stay consistent with index_and_vectorize and keep the SQL ecosystem (sqlite3, parsing libs) cohesive in one language. CLAUDE.md's "always use uv" applies.
4. **What happens when a JSON is deleted from disk**: sync currently only adds/updates. Should `--reset` be the only way to clear stale rows, or should sync detect orphans? **Default**: orphan detection requires walking the dir twice and is mostly cosmetic — defer until needed; `--reset` is the escape hatch.
5. **Concurrent writes**: SQLite supports one writer at a time. If sync_sql runs while indexing is also writing somewhere, do we need locking? Phase 1 has no other SQL writers, so this is a non-issue until Phase 3.

---

## Future phase notes (sketches, not specs)

These get fleshed out when their phase starts.

### Phase 2: SQL pre-filter in cmd/web

- `cmd/web` parses the query for structured tokens (camera names, year mentions, aperture-shaped substrings)
- Looks up photo_ids matching those structured filters via SQL
- Passes that file_path subset to `tools/search.sh --json-dir <dir>` as a hint
- Or: search.py learns to accept a `--restrict-to <comma-separated-ids>` flag

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

---

## Decisions log

Track what got chosen and why as phases land.

| Date | Decision | Phase | Rationale |
|------|----------|-------|-----------|
| _(populate as decisions are made)_ | | | |
