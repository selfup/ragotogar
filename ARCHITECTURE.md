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
              │  Postgres + pgvector       │
              │  • photos / exif / descriptions / inference / thumbnails
              │  • chunks (vector embeddings, schema-per-camera shard)
              │  • descriptions.fts (tsvector — keyword recall)
              │  • verify_cache  (query × photo_id → verdict)
              └──────────────┬─────────────┘
                  ┌──────────┼──────────┐
                  ↓          ↓          ↓
              x100vi       xt5         a7      ← Postgres schemas, one per camera
              .chunks    .chunks    .chunks      cross-shard query = UNION ALL
                  └──────────┼──────────┘
                             ↓
              ┌────────────────────────────┐
              │  LLM verify (parallel)     │  ← cached in verify_cache
              └────────────────────────────┘
```

### Six design pillars

| Pillar | What it solves | Implementation |
|--------|----------------|----------------|
| 1. Bounded-complexity vector indexes | Recall stability + per-shard re-index cost | One pgvector HNSW index per camera schema (~3-10K rows each) instead of one monolith over 30K |
| 2. Incremental re-indexing | Wall-clock cost of adding photos | Each schema re-indexes independently; adding a month of X100VI ≈ minutes, not hours |
| 3. Forced parallel workloads | Throughput before LLM is involved | UNION ALL across N schemas → N× retrieval parallelism; verify is already 8-way; compounds |
| 4. Cache layer | Repeat-query cost | Postgres-backed: `(query, photo_id) → verdict` for verify, `(query, mode, shard) → photo_ids` for retrieval; invalidated on shard re-index timestamp |
| 5. SQL frontend | Structured filters + cache lookup | Postgres over EXIF metadata; pre-filter joins into the vector query — single roundtrip, no app-side intersection |
| 6. SQL is also the data store | Cache, portable moves, deep analysis | Single DB for everything photo-related; SQL aggregations enable "lens diversity by year"-shaped questions; pg_dump for portability |

---

## Key design decisions

These need to be pinned down before each relevant phase. Not all need answers now — flagged with the phase that depends on them.

### A. Stable photo identity (Phase 7)

`file_path` is the de-facto key today. Files move → indexes break. Need a path-independent ID. The `photos.id` column already exists for this — Phase 7 populates it with a stable hash; until then it's the same string as `name`.

| Choice | Pros | Cons |
|--------|------|------|
| **SHA256 of file body** | True content identity; survives moves *and* renames; enables dedup detection | One-time hash cost (~minutes for 30K); changes if photo is re-edited |
| **Composite of EXIF date + filename + size** | Cheap; survives moves; survives reprocessing; doesn't read file body | Changes if EXIF is edited (rare); two distinct photos with identical EXIF+filename+size collide (vanishingly rare) |
| **UUID generated at describe time** | Cheap; stable through edits | Doesn't survive re-describe unless we propagate; no natural dedup |

**Default recommendation**: composite hash of `(exif_date_time_original, original_filename, file_size)`. Cheap, stable for the 99% case, no body read needed. Falls back to file body SHA256 if any composite component is missing.

### B. Shard key (Phase 6)

| Choice | Shard count | Pros | Cons |
|--------|-------------|------|------|
| `camera_model` (X100VI / X-T5 / A7…) | ~4-8 | Natural; aligns with how user thinks; fast camera-explicit routing | Imbalanced if one camera dominates (e.g. 25K X100VI + 500 each other) |
| `make` (Fujifilm / Sony / DJI) | ~3-5 | Coarser; fewer shards; less imbalanced if user has multiple bodies of same brand | Loses model-level routing |
| `year` | ~5-10 | Time-balanced if shooting volume is roughly steady | Loses camera-explicit routing; rebalances naturally with new years |
| Hybrid (`make+year`) | ~10-30 | Both axes bounded | Operational overhead grows with shard count |

**Default recommendation**: `camera_model` for the first cut. Re-evaluate if X100VI shard alone exceeds ~10K photos.

### C. Cache invalidation granularity (Phase 5)

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
| **2. Postgres + pgvector foundation** | Single Postgres DB replaces SQLite library.db + LightRAG `.rag_index/`. pgvector for similarity, tsvector for keyword recall. Drops the lightrag dep entirely; ~200× faster indexing (no entity extraction). | medium |
| **3. Retire Python — rewrite `tools/` as Go** | Single-language codebase (Go). No `tools/.venv`, no `uv`, no `pyproject.toml`. Drops the lightrag-hku / openai / httpx / numpy / pytest deps. | medium |
| **4. SQL pre-filter in the search path** | Structured filter merged into the pgvector query — single SQL roundtrip from query → ranked photos | small |
| **5. Verify-result cache** | `verify_cache(query, photo_id, verdict)` table in the same DB; repeat queries free | small |
| **6. Camera-sharded schemas + parallel fan-out** | One Postgres schema per camera; cross-shard query is `UNION ALL`; per-shard re-indexing is `TRUNCATE shard.chunks; INSERT …` | small |
| **7. Stable photo IDs + path-portability migration** | Move/rename safe; dedup detection | medium |
| **8. Cross-shard analysis tooling (SQL aggregations)** | "Most-used aperture in 2024", lens diversity reports, etc. | small |

Highest near-term leverage: Phase 2 (foundation swap; everything downstream simplifies on top of it).

---

# Phase 2: Postgres + pgvector foundation — detailed spec

## Goal

Replace SQLite (`tools/.sql_index/library.db`) and LightRAG (`tools/.rag_index/`) with a single Postgres database using the pgvector extension. End state: one connection string opens everything photo-related — typed metadata, FTS, vector embeddings, BLOBs, and forward-looking verify-cache / shard-routing tables. The lightrag Python dependency goes away entirely.

End state: `psql $LIBRARY_DSN` is the entry point to the entire library. `pg_dump | rsync | pg_restore` is the multi-machine sync workflow.

## Why this shape

The naive (pure-vector) mode is the validated default for our queries — graph-aware modes underperform on this corpus. LightRAG's value-add is the entity-extraction + graph-traversal layer; once you don't use it, what's left is "chunk → embed → vector store," which pgvector does directly with one extension and no Python middleware. Killing LightRAG also kills the entity-extraction LLM call per chunk, which is where indexing time goes — drops from ~10s/photo to ~50ms/photo (just the embedding call).

Going Postgres for vectors and keeping SQLite for everything else would be a half-measure — two storage systems, two backup stories, two query languages. Moving the typed metadata across at the same time is a one-line schema delta (TEXT/BLOB/REAL → TEXT/BYTEA/REAL) and consolidates everything.

**Bigger downstream consequence**: dropping LightRAG removes the only Python dependency that's hard to replace. After Phase 2 the rest of `tools/` is thin glue — chunk text, POST to LM Studio for embeddings, INSERT/SELECT against pgvector. None of that requires Python. Phase 3 retires Python entirely (single-language Go codebase: no `tools/.venv`, no `uv`, no `pyproject.toml`).

## Non-goals

- **Not** moving original photos into Postgres (size; wrong tool — same as before)
- **Not** introducing entity extraction in any form (LightRAG goes; nothing replaces it). Photo-level facts live in `descriptions` + `exif`; if cross-photo entity reasoning is ever needed, revisit then.
- **Not** running Postgres remotely by default — a local Homebrew install is the baseline (Docker is supported but not the canonical path).
- **Not** stable photo IDs (still Phase 7 — `photos.id` stays = `name` until then)
- **Not** the Python → Go rewrite — Phase 2 leaves `tools/` Python in place (just thinner, calling pgvector instead of LightRAG). Phase 3 does the language consolidation.

## Schema

One Postgres database, default DSN `postgres://localhost/ragotogar`. Tables mirror the current SQLite shape with native types where useful, plus the new `chunks` table.

```sql
CREATE EXTENSION IF NOT EXISTS vector;

CREATE TABLE photos (
    id            TEXT PRIMARY KEY,
    name          TEXT NOT NULL UNIQUE,
    file_path     TEXT,
    file_basename TEXT,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_photos_name ON photos(name);

CREATE TABLE exif (
    photo_id              TEXT PRIMARY KEY REFERENCES photos(id) ON DELETE CASCADE,
    camera_make           TEXT,
    camera_model          TEXT,
    lens_model            TEXT,
    lens_info             TEXT,
    date_taken            TIMESTAMP,           -- richer than TEXT — EXTRACT(year/month) now native
    date_taken_year       SMALLINT,            -- denormalized, kept for cheap filtering
    date_taken_month      SMALLINT,
    focal_length_mm       REAL,
    focal_length_35mm     REAL,
    f_number              REAL,
    exposure_time_seconds REAL,
    iso                   INTEGER,
    exposure_compensation REAL,
    exposure_mode         TEXT,
    metering_mode         TEXT,
    white_balance         TEXT,
    flash                 TEXT,
    image_width           INTEGER,
    image_height          INTEGER,
    gps_latitude          DOUBLE PRECISION,
    gps_longitude         DOUBLE PRECISION,
    artist                TEXT,
    software              TEXT
);
CREATE INDEX idx_exif_camera     ON exif(camera_model);
CREATE INDEX idx_exif_make       ON exif(camera_make);
CREATE INDEX idx_exif_date       ON exif(date_taken);
CREATE INDEX idx_exif_year_month ON exif(date_taken_year, date_taken_month);
CREATE INDEX idx_exif_iso        ON exif(iso);
CREATE INDEX idx_exif_aperture   ON exif(f_number);
CREATE INDEX idx_exif_focal      ON exif(focal_length_mm);
CREATE INDEX idx_exif_artist     ON exif(artist);

-- Generated tsvector replaces FTS5 — same recall, native to the JOINs we already do.
CREATE TABLE descriptions (
    photo_id          TEXT PRIMARY KEY REFERENCES photos(id) ON DELETE CASCADE,
    subject           TEXT,
    setting           TEXT,
    light             TEXT,
    colors            TEXT,
    composition       TEXT,
    full_description  TEXT,
    fts               tsvector GENERATED ALWAYS AS (
                        to_tsvector('english',
                          coalesce(subject,'')          || ' ' ||
                          coalesce(setting,'')          || ' ' ||
                          coalesce(light,'')            || ' ' ||
                          coalesce(colors,'')           || ' ' ||
                          coalesce(composition,'')      || ' ' ||
                          coalesce(full_description,''))
                      ) STORED
);
CREATE INDEX idx_descriptions_fts ON descriptions USING gin(fts);

CREATE TABLE inference (
    photo_id     TEXT PRIMARY KEY REFERENCES photos(id) ON DELETE CASCADE,
    raw_response TEXT,
    model        TEXT,
    preview_ms   INTEGER,
    inference_ms INTEGER,
    described_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE thumbnails (
    photo_id   TEXT PRIMARY KEY REFERENCES photos(id) ON DELETE CASCADE,
    bytes      BYTEA NOT NULL,
    width      INTEGER NOT NULL DEFAULT 1024,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- The new bit: chunks + vectors in the same DB.
CREATE TABLE chunks (
    photo_id   TEXT NOT NULL REFERENCES photos(id) ON DELETE CASCADE,
    idx        SMALLINT NOT NULL,         -- chunk ordinal within the photo's document
    text       TEXT NOT NULL,
    embedding  vector(768) NOT NULL,      -- nomic-embed-text-v1.5 dim
    PRIMARY KEY (photo_id, idx)
);
CREATE INDEX idx_chunks_embedding ON chunks USING hnsw (embedding vector_cosine_ops);
```

Phase 6 sharding turns `chunks` into per-camera schemas (`x100vi.chunks`, `xt5.chunks`, …) and search becomes a `UNION ALL`. The other tables stay in the public schema — they're the photo registry, not shard data.

## Indexing — what replaces `index_and_vectorize.py`

The Python script collapses to chunk + embed + INSERT. No entity extraction, no graph storage, no LightRAG.

```python
# tools/index_and_vectorize.py (post-swap, sketch)

CHUNK_TOKENS = 1200
OVERLAP = 100

async def index(conn, embed):
    photos = await conn.fetch("SELECT id, name FROM photos ORDER BY name")
    for row in photos:
        doc = build_document(await fetch_photo_dict(conn, row["name"]))
        chunks = chunk_text(doc, CHUNK_TOKENS, OVERLAP)   # ~1-3 per photo
        embeddings = await embed(chunks)                  # batch call to LM Studio
        await conn.execute("DELETE FROM chunks WHERE photo_id = $1", row["id"])
        await conn.executemany(
            "INSERT INTO chunks (photo_id, idx, text, embedding) VALUES ($1, $2, $3, $4)",
            [(row["id"], i, t, e) for i, (t, e) in enumerate(zip(chunks, embeddings))],
        )
```

Re-indexing in place: TRUNCATE chunks, re-run. Or per-photo replace as above. No KV stores, no GraphML, no JSON-glob.

## Searching — what replaces `search.py`

```sql
-- naive (current default): pure vector similarity
SELECT p.name, 1 - (c.embedding <=> $1) AS similarity
FROM chunks c JOIN photos p ON p.id = c.photo_id
ORDER BY c.embedding <=> $1
LIMIT 30;

-- with structured pre-filter (folds Phase 4 into a single roundtrip)
SELECT p.name, 1 - (c.embedding <=> $1) AS similarity
FROM chunks c JOIN photos p ON p.id = c.photo_id
JOIN exif e ON e.photo_id = p.id
WHERE e.camera_model = 'X100VI' AND e.date_taken_year = 2024
ORDER BY c.embedding <=> $1
LIMIT 30;

-- keyword recall (replaces FTS5)
SELECT p.name FROM descriptions d JOIN photos p ON p.id = d.photo_id
WHERE d.fts @@ plainto_tsquery('english', $1) LIMIT 30;
```

`search.py` becomes a thin runner around these queries. The verify path (LLM yes/no over candidate text) stays — pulls indexable text from the same DB.

## Go-side changes

Both `cmd/describe` and `cmd/web` swap drivers: `modernc.org/sqlite` → `jackc/pgx/v5`. Schema authority moves out of `cmd/describe/schema.go` (Go const string) and into a single `library/schema.sql` checked in at the repo root, applied at process start by both binaries via `pgx.Conn.Exec(string(schemaSQL))`. Keeps schema reproducible across both Go commands and the Python tools.

`insertPhoto` becomes one transaction across the same five tables (photos / exif / descriptions / inference / thumbnails). Parameter placeholders `?` → `$1`, `$2`, … . Everything else (parsing, EXIF normalization, thumbnail bytes) is unchanged.

`cmd/web` photo handler / BLOB stream: same shape, replace SQLite calls with pgx. The `humanDate` / `nl2br` / `shutterFraction` template helpers don't change. The `photoHTML` template doesn't change.

## Local Postgres setup

**Canonical: Homebrew.** No daemon to manage beyond `brew services`, no container runtime needed.

```bash
brew install postgresql@18 pgvector
brew services start postgresql@18

createdb ragotogar
psql ragotogar -c 'CREATE EXTENSION vector;'
export LIBRARY_DSN=postgres:///ragotogar     # local socket, no password
```

The pgvector formula installs the extension into `/opt/homebrew/share/postgresql@18/extension/` so the running cluster picks it up automatically. `brew services restart postgresql@18` if you need to pick up a new extension version.

**Fallback: Docker** (only if brew isn't an option on the host):

```bash
docker run -d --name pg -p 5432:5432 \
  -e POSTGRES_DB=ragotogar -e POSTGRES_HOST_AUTH_METHOD=trust \
  -v pgdata:/var/lib/postgresql/data \
  pgvector/pgvector:pg18
export LIBRARY_DSN=postgres://localhost/ragotogar
```

Both paths land at the same DSN — code doesn't care which is running.

## Migration from existing SQLite

Per the standing "not worried about old data" rule: nuke and re-run is the default path. `cmd/describe` against the photo dir re-populates everything from scratch (faster now since no entity extraction; ~minutes for 300 photos, ~hours for 30K instead of days).

If a one-shot migration is genuinely needed, `tools/sqlite_to_pg.py` reads the existing `library.db`, INSERTs into Postgres, and re-runs the embedding step (entity-extraction-free, so cheap). Defer until asked.

## Tests

- **Go-side**: replace the in-memory SQLite test DB with a `pgx.Conn` against a transient Postgres database. Each test package creates a uniquely-named DB at setup (`CREATE DATABASE test_<random>`), applies the schema, runs against it, and drops it at teardown. Connection target = `LIBRARY_DSN` (the user's local brew Postgres) with the database segment overridden.
- **Python-side**: same shape. `pytest` fixture creates a temp DB per session, applies the schema, drops on teardown.
- Why not `testcontainers-go` / `pytest-postgresql`: both pull in Docker, which we're explicitly avoiding for the local dev story. The temp-DB-per-package pattern is a few lines of helper code and works against the brew-installed cluster.
- All existing test cases (insert roundtrip, idempotency, cascade, FTS, parsers) port over with the same shape — just point at Postgres instead of `:memory:`.

## Open questions

1. **Go driver**: `pgx/v5` (preferred — typed, fast, idiomatic) vs `lib/pq` (deprecated). Default `pgx`.
2. **Python driver**: `asyncpg` (async, fast, what we'd use given LightRAG's async style) vs `psycopg[c]`. Default `asyncpg` + `pgvector` for the vector type adapter.
3. **HNSW vs IVFFlat**: HNSW is the modern default; better recall/QPS tradeoff at our scale. Ship HNSW.
4. **Tokenizer for chunking**: `tiktoken`'s cl100k or a simpler char-window? LightRAG used the former. Keep tiktoken to preserve chunk boundaries similar to today.
5. **Connection config**: `LIBRARY_DSN` env var, defaulting to `postgres://localhost/ragotogar`. Override on a per-machine basis.
6. **Embedding model**: stays `nomic-embed-text-v1.5` (768-dim) so the corpus doesn't need re-embedding if Phase 2 is the only swap; revisit when batch-changing models.

## Cost of failure

Postgres + pgvector is well-trodden territory; the schema is straightforward; the search queries are 5-line SQL. Risk is concentrated in:

- **Chunking equivalence**: if the new chunker produces materially different boundaries than LightRAG's, recall changes. Validate with a side-by-side query set before retiring the LightRAG dir.
- **Daemon-not-running**: every command needs Postgres up. Document `brew services start postgresql@18` prominently in the README; add `services list` to the troubleshooting blurb.
- **Backup discipline**: SQLite's `cp library.db` is foolproof; `pg_dump` requires a habit. Document a `scripts/backup.sh` wrapper.

If Phase 2 doesn't work out, revert: nothing in the SQLite + LightRAG world is destroyed by this work — the swap is at the storage layer, the photo files and the describe pipeline are unchanged.

---

## Phase notes (sketches, not specs)

These get fleshed out when their phase starts.

### Phase 3: Retire Python — rewrite `tools/` as Go

After Phase 2 the Python in `tools/` is thin glue around pgvector — chunk text, POST to LM Studio for embeddings, INSERT/SELECT against Postgres. None of it requires Python.

- **Chunking**: word-window splitter in Go (a `tiktoken-go` port exists if we want token-based boundaries)
- **Embedding**: `net/http` POST to LM Studio's `/v1/embeddings` (replaces the `openai` async client)
- **pgvector binding**: `github.com/pgvector/pgvector-go` — registers the `vector` type with `pgx`
- **Verify**: same `net/http` to LM Studio with the yes/no prompt; goroutine pool replaces `asyncio.gather`
- **CLAUDE.md**: drop the Python rules section

End state: each tool becomes a Go binary or root-module package — `cmd/index`, `cmd/search`, etc. The `tools/` directory either disappears or holds shell wrappers only. Drops deps: `lightrag-hku`, `httpx`, `numpy`, `openai`, `pytest`.

Open: where to place the new commands — own modules under `cmd/` (own go.mod each, like `cmd/describe`), or root-module packages alongside `cmd/web`?

### Phase 4: SQL pre-filter in the search path

After Phase 2, this collapses to a single SQL query:

```sql
SELECT p.name, 1 - (c.embedding <=> $1) AS similarity
FROM chunks c JOIN photos p ON p.id = c.photo_id
JOIN exif e ON e.photo_id = p.id
WHERE e.camera_model = ANY($2)         -- camera tokens parsed from query text
  AND e.date_taken_year = ANY($3)      -- year tokens
  AND ($4::real IS NULL OR e.f_number BETWEEN $4 - 0.1 AND $4 + 0.1)
ORDER BY c.embedding <=> $1
LIMIT 30;
```

`cmd/web` parses the query for camera names, year mentions, aperture-shaped substrings; binds them as parameters. No app-side intersection.

### Phase 5: Verify cache

```sql
CREATE TABLE verify_cache (
    query             TEXT NOT NULL,
    photo_id          TEXT NOT NULL REFERENCES photos(id),
    verdict           BOOLEAN NOT NULL,
    verified_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    shard_indexed_at  TIMESTAMPTZ,        -- for invalidation against shard re-indexes
    PRIMARY KEY (query, photo_id)
);
```

Lookup: `(query, photo_id)` and `verified_at > shards.last_indexed_at[shard_of_photo]`. If hit, skip LLM call.

### Phase 6: Camera-sharded schemas

- `chunks` moves out of `public` into per-camera schemas: `x100vi.chunks`, `xt5.chunks`, …
- New table `shards (name, schema, last_indexed_at, photo_count)` in `public` for the registry
- Cross-shard query: `SELECT … FROM x100vi.chunks UNION ALL SELECT … FROM xt5.chunks ORDER BY …`
- Per-shard re-index: `TRUNCATE x100vi.chunks; INSERT …` — bounded to one camera's data

### Phase 7: Stable photo IDs

- Compute composite hash on next describe run (cheap — just EXIF + name + size)
- Migration: walk existing photos, populate `id` column from composite, leaving `name` as the human-readable handle
- chunks re-embed needed if name changes; if `id` is the FK and `name` is stable, no re-embed required

### Phase 8: Analysis tooling

- `tools/analyze.py` with subcommands:
  - `lens-stats` — most-used lenses by year
  - `aperture-distribution` — histogram
  - `coverage-by-camera-month` — heatmap data
  - `vocabulary` — most common tokens in the FTS index
- Outputs JSON or Markdown tables; consumed by future "library dashboard" web view
