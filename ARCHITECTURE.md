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

| Phase | Status | Delivers | Dependencies | Effort |
|-------|--------|----------|--------------|--------|
| **1. SQLite metadata index** | ✅ shipped (2026-04-28) | Fast structured filters; foundation for caching, sharding, deep analysis | none | small |
| **1.5. Unified SQL data model** | not started | One `library.db` containing inference / EXIF / descriptions / MD / HTML / thumbnails; originals + LightRAG stay on disk | Phase 1 | medium |
| **2. SQL pre-filter in cmd/web search path** | not started | "SQL filter → vector → verify" pipeline end-to-end | Phase 1.5 | small |
| **3. Verify-result cache in SQL** | not started | Repeat queries become free | Phase 1 | small |
| **4. Camera-sharded LightRAG indexes + super-graph router** | not started | Bounded shards, parallel fan-out, incremental re-indexing | Phase 1 (for shard registry) | medium |
| **5. Stable photo IDs + path-portability migration** | not started | Move/rename safe; dedup detection | Phase 1 | medium |
| **6. Cross-shard analysis tooling (SQL aggregations)** | not started | "Most-used aperture in 2024", lens diversity reports, etc. | Phase 1 | small once SQL is in |

Phase 1 done. Phase 1.5 (unified SQL data model) goes next — it makes the rest of the rollout SQL-native and shrinks the operational surface to one file. Phase 2's pre-filter then composes cleanly on top.

---

# Phase 1: SQLite metadata index — detailed spec

## Status: ✅ shipped (2026-04-28)

Landed across 4 commits, sliced to keep each diff small and reviewable:

| Commit | Slice |
|--------|-------|
| `dd2ff9d` | Schema (photos / exif / descriptions) + JSON parser + idempotent UPSERT-driven `tools/sql_sync.py` + `--reset` |
| `535e52a` | FTS5 virtual table over descriptions with porter+unicode61 stemming, plus AI/AD/AU triggers (idempotent on re-sync) |
| `e26ec80` | 15-test pytest suite covering parsers, schema, sync, FTS, cascade-delete, missing-fields robustness; `tools/test.sh` runner |
| `8e2b3a0` | `tools/sql_query.sh` ad-hoc helper + wired `sql_sync.sh` into `scripts/dir_photos.sh` and `full_run.sh` after the cashier step |

Verified end-to-end against `describe_test/` (182 X100VI photos): all parsers correct, FTS porter stemming (`tree` ↔ `trees`) works, `NEAR(a b, N)` and boolean queries work, re-syncs stay idempotent at exactly N rows per table.

What's in the schema beyond the original spec: `exposure_compensation REAL` (always present in the X100VI corpus, useful for filters).

What was deferred to later slices/phases:
- mtime-vs-`updated_at` freshness short-circuit on resync — currently always-upsert, fast enough at 182 rows
- orphan detection when a JSON disappears from disk — `--reset` is the escape hatch
- Phase 2 search integration

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

## Open questions — resolved

1. **Dependencies**: stdlib `sqlite3` only. `pytest` added as a dev dep for the test suite. No ORM. ✅
2. **Migration framework**: handwritten — `schema_version` row inserted via `INSERT OR IGNORE`. Slice-1 → slice-2 upgrade was handled by `IF NOT EXISTS` + the AFTER UPDATE trigger backfilling FTS on next sync. ✅
3. **Where to put it**: `tools/sql_sync.py` (Python, next to existing tools, runs via `uv run --project tools`). ✅
4. **JSON deletion → orphan rows**: deferred. `--reset` is the escape hatch; orphan detection waits until needed. ✅ (deferred by design)
5. **Concurrent writes**: non-issue in Phase 1 — only `sql_sync` writes. Re-evaluate when Phase 3 introduces the verify cache.

---

# Phase 1.5: Unified SQL data model — detailed spec

## Status: not started

## Goal

Make SQLite the source of truth for all photo-derived data. Originals (RAF/JPEG) and the LightRAG index stay on disk for size and tooling reasons; everything else — describe inference output, EXIF, parsed fields, descriptions, cashier MD/HTML, thumbnail JPGs — lives in `library.db`.

End state: a working library is one file. Move it between machines with `cp`. No `describe_*` dirs, no MD/HTML/JPG sidecars to keep in sync with photo identity.

## Why now (before Phase 2)

Phase 2 (SQL pre-filter in `cmd/web`) needs `cmd/web` to look up photos by name and serve their rendered HTML. Today that's a file-path lookup; tomorrow it should be a `SELECT`. Doing 1.5 first means Phase 2's plumbing is already SQL-native and we don't refactor it twice.

## Non-goals

- **Not** moving original photos into SQL (size; wrong tool — 30K × ~30MB ≈ 900GB)
- **Not** migrating LightRAG's index (its own backend; orthogonal)
- **Not** implementing stable photo IDs (Phase 5 — `photos.id` stays = `name` until then)

## Schema delta from Phase 1

```sql
-- Cashier outputs
CREATE TABLE rendered (
    photo_id    TEXT PRIMARY KEY REFERENCES photos(id) ON DELETE CASCADE,
    md          TEXT,                  -- cashier markdown
    html        TEXT,                  -- cashier full HTML page
    thumbnail   BLOB,                  -- 1024px JPG bytes (image/jpeg)
    rendered_at TEXT NOT NULL DEFAULT (datetime('now'))
);

-- describe-only fields that don't fit photos / exif / descriptions
CREATE TABLE inference (
    photo_id     TEXT PRIMARY KEY REFERENCES photos(id) ON DELETE CASCADE,
    raw_response TEXT,                 -- full LLM output before parse_fields
    model        TEXT,                 -- e.g. qwen3-vl-8b
    preview_ms   INTEGER,
    inference_ms INTEGER,
    described_at TEXT NOT NULL DEFAULT (datetime('now'))
);

-- Slice 5 (cutover) drops the now-unused path columns from photos:
-- ALTER TABLE photos DROP COLUMN md_path;
-- ALTER TABLE photos DROP COLUMN html_path;
-- ALTER TABLE photos DROP COLUMN jpg_path;
-- ALTER TABLE photos DROP COLUMN json_path;
```

Phase 1's `photos` / `exif` / `descriptions` / `descriptions_fts` schemas are unchanged through slices 1–4. Slice 5 drops the path columns.

## Slicing

Five slices. Slices 1–4 are reversible (system runs in dual-write mode); slice 5 is the cutover.

| Slice | Diff | Reversible? |
|-------|------|-------------|
| 1. Schema + migration tool | Adds `rendered`, `inference` tables. `tools/sql_migrate.py` walks existing path columns and ingests file bytes (MD/HTML as TEXT, JPG as BLOB) and JSON inference fields. Pytest coverage. | Yes — purely additive |
| 2. `cmd/describe` writes to SQL | Add pure-Go SQLite driver (`modernc.org/sqlite`). After producing the photo struct, INSERT into photos + exif + descriptions + inference. Keep JSON write for now (dual-write). | Yes — JSONs still produced |
| 3. `cmd/cashier` reads + writes SQL | `cashier photo` reads description from SQL by name, writes MD + thumbnail BLOB into `rendered`. `cashier build` reads MD from SQL, writes HTML into `rendered`. File output behind `-files` flag for transition. | Yes — `-files` keeps disk artifacts |
| 4. `cmd/web` serves from SQL | `GET /<name>.html` → `SELECT html FROM rendered`. `GET /<name>.jpg` → BLOB stream with `Content-Type: image/jpeg`. Static-file serve retired. | Yes — old code can sit behind a flag during soak |
| 5. Cutover | Drop JSON write from `cmd/describe`. Drop `-files` flag from cashier. Drop path columns from `photos`. Update README, `dir_photos.sh`, `full_run.sh` to reflect the single-file workflow. | No — point of no return |

## Migration of existing data

`tools/sql_migrate.py` (slice 1, one-shot — not pipeline-wired):

```
sql_migrate.py [--dry-run]

  For each photos row:
    • If md_path exists on disk: read, REPLACE INTO rendered.md
    • If html_path exists:       read, REPLACE INTO rendered.html
    • If jpg_path exists:        read bytes, REPLACE INTO rendered.thumbnail
    • If json_path exists:       parse, REPLACE INTO inference
                                 (preview_ms, inference_ms, model, raw inference text)

  Idempotent. --dry-run validates without writing. Logs per-photo and
  reports row counts at end.
```

Run once on the existing 182-row library and verify counts match. After cutover (slice 5) the migration tool stops being relevant and can be retired.

## What `tools/sql_sync.py` becomes

Inverts purpose. Today it walks JSONs → SQL; after slice 2 there are no JSONs to walk. Two options:

a) **Retire** — slice 2 makes describe write direct, so sync has no inputs.
b) **Repurpose as `tools/sql_export.py`** — dumps SQL rows back to JSON files for backup, diff, or git review.

**Default**: rename to `sql_export.py` and run in the export direction. Keeps the diff/grep workflow available on demand. Trivial to retire later if unused.

## Tests

| Test | Covers |
|------|--------|
| `test_rendered_roundtrip` | INSERT + SELECT MD/HTML/thumbnail; bytes match |
| `test_blob_large` | 500KB JPG BLOB round-trips without truncation |
| `test_inference_table` | Types and round-trip for preview_ms / inference_ms / raw_response |
| `test_migrate_idempotent` | Re-running `sql_migrate.py` produces stable rows |
| `test_cascade_delete_through_rendered` | DELETE FROM photos cascades to rendered + inference |
| Go-side: `cmd/describe` SQL writer | Temp DB; verify all four tables populated for a known struct |
| Go-side: `cmd/cashier` SQL reader/writer | Render via SQL path, compare HTML byte-for-byte against file path |
| Go-side: `cmd/web` BLOB serve | HTTP GET returns thumbnail bytes with `image/jpeg` |

## Open questions

1. **Go SQLite driver**: `modernc.org/sqlite` (pure-Go, transpiled — preferred) or `mattn/go-sqlite3` (cgo — faster but adds toolchain dep). Default modernc; revisit if benchmarks show pathological slowness.
2. **Concurrent writes**: today only `sql_sync` writes. After 1.5, both `cmd/describe` and `cmd/cashier` write, possibly in parallel batches. SQLite WAL mode + `PRAGMA busy_timeout=5000` should cover this. Verify under cashier's 8-worker batch before slice 5.
3. **BLOB streaming in cmd/web**: full-load is fine for 200KB thumbnails. Revisit only if originals ever land in SQL (not planned).
4. **Backup workflow post-cutover**: document `cp library.db backup-$(date).db` in README. SQLite's atomic-on-checkpoint behavior makes this safe even with the server running; `.dump` is the bulletproof option.

## Cost of failure

Each slice 1–4 is independently revertible. Worst case after slice 4 with 1.5 not panning out: dual-write state (BLOBs in DB + files on disk), pipelines work either way. Drop the BLOBs and the migration tool, no harm done.

Slice 5 is irreversible. Don't ship it until slices 1–4 have been running side-by-side through at least one full describe → cashier → index pass.

---

## Future phase notes (sketches, not specs)

These get fleshed out when their phase starts.

### Phase 2: SQL pre-filter in cmd/web

Assumes Phase 1.5 — `cmd/web` already serves from `library.db`, no JSON/MD path lookups in the search path either.

- `cmd/web` parses the query for structured tokens (camera names, year mentions, aperture-shaped substrings)
- Looks up photo_ids matching those structured filters via SQL
- Passes the candidate set to `search.py --restrict-to <names>` (search.py reads description text from SQL after 1.5, so name-based restriction is straightforward)

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
| 2026-04-28 | Add `exposure_compensation REAL` to the `exif` schema | 1 | Always present in the X100VI corpus and useful for filtering ("under-exposed shots", "EV-biased frames") |
| 2026-04-28 | FTS5 tokenizer = `porter unicode61` | 1 | Porter stemming makes `tree` match `trees`, `shadow` match `shadows`; unicode61 handles case folding and diacritics |
| 2026-04-28 | Always-upsert on every sync (no mtime freshness check) | 1 | Sub-second on 182 rows; revisit when sync time becomes noticeable |
| 2026-04-28 | Orphan detection deferred — `--reset` is the escape hatch | 1 | Two-pass walk has cost without near-term value; users curate JSON dirs by hand today |
| 2026-04-28 | All sidecar paths (`md_path`, `html_path`, `jpg_path`, `json_path`) stored as absolute | 1 | Mirrors the JSON's own `data.path` (already absolute); avoids cwd-dependent reads downstream |
| 2026-04-28 | Add Phase 1.5: unify all photo-derived data into SQLite before Phase 2 | 1.5 | Sidecar files desync on path changes and Phase 2 needs SQL-native `cmd/web` lookups anyway. Unifying first removes the path-as-key fragility for derived artifacts and makes the working library a single `library.db` that's portable with `cp`. |
