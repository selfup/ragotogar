# Ragotogar

_Research preview :warning: this project is under heavy discovery work and development_

**RAG Photo Organizer** — a local-LLM photo library with vector search. *(Yes, it's a palindrome.)*

A collection of utility shell scripts and Go programs to: organize, normalize, describe, render, and search media.

## Requirements

- **macOS** — the organizer uses macOS-specific syscalls for file birth time
- **[LM Studio](https://lmstudio.ai/)** — local LLM inference server (vision + text + embedding models)
- **[exiftool](https://exiftool.org/)** — EXIF metadata extraction (`brew install exiftool`)
- **[ImageMagick](https://imagemagick.org/)** — image resizing for LLM previews (`brew install imagemagick`)
- **[rclone](https://rclone.org/)** — NAS sync (`brew install rclone`)
- **Postgres 18 + pgvector** — `./scripts/bootstrap.sh` installs both via Homebrew, starts the cluster, creates the library DB, and loads the vector extension. Re-run any time; idempotent.
- **Go 1.26+** — entire pipeline is pure Go (`go run`, no build step). No Python.

## Quick start

```bash
# One-time, on a fresh checkout
./scripts/bootstrap.sh           # postgres + pgvector + ragotogar DB + schema

# Each time
PHOTO_DIR=/path/to/photos ./full_run.sh
```

## Pipeline

Each step feeds the next:

| Step | What | Script |
|------|------|--------|
| 1. **Organize** | Sort media into type folders (JPEG, RAW, MOV...) and date subfolders | `scripts/organize.sh` |
| 2. **Describe** | Send each photo to a vision LLM, write photo + EXIF + parsed fields + 1024px thumbnail BLOB into Postgres | `scripts/photo_describe.sh` |
| 3. **Index** | Read each photo from Postgres, chunk + embed via LM Studio, INSERT into the `chunks` table (pgvector) | `scripts/index.sh` |
| 4. **Search** | pgvector cosine similarity (`ORDER BY embedding <=> $1`); optional LLM verify pass | `scripts/search.sh` |
| 5. **Browse** | Web server — search box + thumbnail grid; per-photo pages render on-demand from SQL | `scripts/web.sh` |

Steps are independent — you can run search without ever organizing, or describe without syncing to a NAS. The typical full flow is 1 → 2 → 3 → 4 → 5.

## Components

| Component | Location | Description |
|-----------|----------|-------------|
| Go Media Organizer | `cmd/organize` | Parallel file organizer: sorts media into type/date folders, reunites sidecars. macOS-only. |
| Photo Describer | `cmd/describe` | Vision LLM descriptions + EXIF metadata + thumbnail JPG → Postgres |
| NAS Sync | `scripts/clone.sh` | rclone-based sync with month/year filtering and `--no-videos` |
| Vector Indexer | `cmd/index` | Chunks + embeds each photo's description, INSERTs into pgvector chunks table |
| Vector Search | `cmd/search` | pgvector cosine similarity over chunks; optional LLM verify pass |
| Web Server | `cmd/web` | Search UI + per-photo HTML pages rendered on-demand from SQL; thumbnail BLOBs streamed from SQL |
| Cashier (markdown) | `cmd/cashier` | General-purpose markdown → HTML renderer with the cashier design system. Not in the photo pipeline; kept for ad-hoc use. |

Plus shell scripts for directory flattening, EXIF date fixing, and a shared config (`.files.env`) as the single source of truth for extension mappings.

## Go Media Organizer (`cmd/organize`)

Parallel media organizer in Go. Uses a worker pool (`runtime.NumCPU()` goroutines) to move files concurrently across all 3 passes.

**Usage:**

```bash
./scripts/organize.sh /path/to/directory

# Use modification time instead of birth time for date folders
./scripts/organize.sh -mtime /path/to/directory
```

**What it does (3 passes):**

1. **Organize by type** — Moves files into folders based on extension (JPEG, RAW, HIF, MOV, MP4, BRAW, NEV, NDF)
2. **Organize by date** — Within each type folder, groups files into date-based subfolders using the file's birth time (e.g. `March11th2026`)
3. **Reunite sidecars** — Finds orphaned sidecar files (dxo, dop, pp3, xml, aac, lrf, mp3) and moves them next to their parent media file. Orphan XML/AAC sidecars default to MP4 (Sony/DJI workflow). Orphan MP3s (no matching WAV) default to AUDIO.

**Supported formats:**

| Folder | Extensions |
|--------|-----------|
| JPEG | jpg, jpeg |
| HIF | hif, heif, heic |
| RAW | raf, arw, nef, cr2, cr3, dng, orf, rw2, pef |
| MOV | mov |
| MP4 | mp4 |
| BRAW | braw |
| NEV | nev |
| NDF | ndf |
| AUDIO | wav |

**Key details:**

- Jobs are sorted by destination and filename before dispatch for deterministic ordering
- All destination directories are pre-created before workers start (no race conditions)
- Sidecars travel with their parent file in the same goroutine
- By default, birth time is used for date folders. If files on a drive all share the same birth time (common when bulk-copied to a new drive), use `-mtime` to organize by modification time instead
- **macOS only** — uses `syscall.Stat_t.Birthtimespec` (the same field Finder displays as "Date Created")
- **AppleDouble `._` files are skipped** — On non-native filesystems (exFAT, FAT32, NTFS — common on external SSDs like the Samsung T9), macOS creates hidden `._*` companion files to store extended attributes and resource forks. These files share the same extension as their parent (e.g. `._DSC00596.JPG`), so the organizer would otherwise treat them as real media files. However, when the real file is moved, macOS automatically cleans up the corresponding `._` file — causing a "no such file or directory" error when the organizer tries to move it separately. All `._` files are skipped in every pass.

## Photo Describer (`cmd/describe`)

Extracts EXIF metadata, generates a 1024px thumbnail, and produces an LLM visual description via LM Studio's vision API. All output (typed EXIF columns, parsed `subject/setting/light/colors/composition/vantage/ground_truth/condition` fields, full description, thumbnail BLOB, model + timing) writes directly to the Postgres library. The `photos` table is the source of truth — no JSON sidecars, no MD/HTML files.

**Usage:**

```bash
./scripts/photo_describe.sh /path/to/photos

# Custom library DSN
./scripts/photo_describe.sh -dsn postgres:///other_db /path/to/photos

# Preview which files would be processed
./scripts/photo_describe.sh -dry-run /path/to/photos

# Re-describe photos already in the DB (UPSERTs over existing rows)
./scripts/photo_describe.sh -force /path/to/photos

# Use a different vision model
./scripts/photo_describe.sh -model mistralai/ministral-3-3b /path/to/photos

# More retry attempts for flaky models
./scripts/photo_describe.sh -retries 5 /path/to/photos
```

**Options:**

| Flag | Description |
|------|-------------|
| `-dsn DSN` | Postgres library DSN (default: `postgres:///ragotogar` or `LIBRARY_DSN` env) |
| `-force` | Re-describe photos already in the DB; UPSERTs across all five tables |
| `-init-only` | Open the DB, apply the schema, and exit (used by `scripts/bootstrap.sh`) |
| `-model NAME` | LM Studio model name (default: `qwen/qwen3-vl-8b` or `LM_MODEL` env) |
| `-dry-run` | List files without calling the LLM or touching the DB |
| `-retries N` | Max retry attempts per image on API failure (default: 3) |
| `-preview-workers N` | Parallel ImageMagick/exiftool workers for preview generation (default: 4) |
| `-inference-workers N` | Parallel LLM inference workers (default: 1). Bump to N to use LM Studio's `--parallel N` continuous batching. Vision inference is more memory-intensive than text — start at 2–4 and watch VRAM/error rate before going higher. |

**Environment variables:**

| Variable | Default | Description |
|----------|---------|-------------|
| `LIBRARY_DSN` | `postgres:///ragotogar` | Postgres connection string (shared by all components) |
| `VISION_ENDPOINT` | falls back to `LM_STUDIO_BASE` | OpenAI-compatible URL for the vision model. Set to a cloud provider when you outgrow local LM Studio. |
| `LM_STUDIO_BASE` | `http://localhost:1234` | Legacy single-endpoint fallback used when none of `VISION_ENDPOINT` / `TEXT_ENDPOINT` / `EMBED_ENDPOINT` is set |
| `LM_MODEL` | `qwen/qwen3-vl-8b` | Vision model name (see `STRATEGIES.md` for model comparison) |
| `CLASSIFY_MODEL` | `mistralai/ministral-3-3b` | Text model for the inline `-classify` flag (only used when `-classify` is passed) |
| `TEXT_ENDPOINT` | falls back to `LM_STUDIO_BASE` | OpenAI-compatible URL for the classifier text model when `-classify` is on |
| `RESIZE_PX` | `1024` | Longest edge resize for preview (also the thumbnail BLOB size) |
| `JPEG_QUALITY` | `85` | JPEG quality for resized preview |

**Library schema:**

`cmd/describe` is the schema authority — it applies `CREATE TABLE IF NOT EXISTS` on every run, so the DB is created on first invocation and migrated forward on subsequent ones. Seven tables, all keyed on `photos.id` (currently equal to `name` until Phase 7 stable IDs):

| Table | Holds |
|-------|-------|
| `photos` | `id`, `name`, `file_path` (original on disk), `file_basename`, timestamps |
| `exif` | Typed columns from EXIF: `camera_make`, `camera_model`, `lens_model`, `date_taken` (ISO 8601 + decomposed year/month), `focal_length_mm`, `f_number`, `exposure_time_seconds`, `iso`, `exposure_compensation`, `gps_latitude/longitude`, etc. Plus a generated `fts tsvector` column over camera/lens/year/software/artist so FTS+vector mode can match queries like `2024` or `X100VI` that live only in metadata (v8). |
| `descriptions` | Parsed `subject / setting / light / colors / composition / vantage / ground_truth / condition`, `full_description` (the raw LLM output), and a generated `fts tsvector` column (English stemmer) for keyword recall. `cmd/search`'s FTS arm concatenates this with `exif.fts` at query time so prose tokens and metadata tokens can co-match. The `condition` column captures wear/age/cleanliness/construction state so queries like "construction site" or "abandoned building" reach the right photos. |
| `thumbnails` | 1024px JPG bytes as a `BYTEA` BLOB. Generated from the same magick output sent to the vision model — no second resize. |
| `inference` | `model`, `preview_ms`, `inference_ms`, `described_at` |
| `chunks` | One row per chunk per photo. `text TEXT` + `embedding halfvec(2560)` (Qwen3-Embedding-4B GGUF). HNSW index on `embedding halfvec_cosine_ops`. Owned by the indexer (`cmd/index`). |
| `verify_cache` | Persistent LLM yes/no cache for the verify pass. Keyed on `(query, photo_id, verify_model)`; `verified_at > inference.described_at` is the freshness check, so re-describing a photo silently invalidates older cached verdicts. Written by `library.VerifyFilter`; consulted by both `cmd/web` and `cmd/search`. |
| `schema_version` | Single-row marker for migrations |

`cmd/describe` itself never touches `chunks` — that's the indexer's table. Re-describing a photo overwrites its photos / exif / descriptions / inference / thumbnails rows but leaves chunks alone; re-running `./scripts/index.sh` regenerates chunks from the fresh description.

**Key details:**

- RAW files (RAF, ARW, NEF, etc.) use the embedded JPEG preview via `exiftool -b -PreviewImage`, avoiding the need for darktable or rawtherapee
- Non-RAW files are resized to 1024px previews via ImageMagick (configurable)
- The same JPG bytes are base64-encoded for the LLM and stored as the thumbnail BLOB — single magick pass
- Skips `._` AppleDouble files and `.DS_Store`
- Exponential backoff with jitter on API failures
- Unique session ID per request to prevent LM Studio KV cache reuse
- Strips `<think>` blocks from reasoning models; detects when model exhausts tokens on reasoning with no content
- Skip-exists checks `SELECT 1 FROM photos WHERE name = $1` (use `-force` to re-describe)
- All five describe-owned tables get UPSERTed inside one transaction per photo
- **Requirements:** [exiftool](https://exiftool.org/), [ImageMagick](https://imagemagick.org/), Postgres + pgvector (`./scripts/bootstrap.sh`), LM Studio running with a vision model loaded

**Ad-hoc SQL:**

```bash
psql -d ragotogar -c "SELECT camera_model, COUNT(*) FROM exif GROUP BY camera_model"
psql -d ragotogar -f /path/to/query.sql
```

## Cashier (`cmd/cashier`)

A general-purpose markdown → HTML renderer that ships the cashier design system (warm-paper editorial style; see `styles.css`). Used to live in the photo pipeline producing per-photo `.md` / `.html` / `.jpg` sidecars; that role is now in `cmd/web` (per-photo pages render on-demand from SQL via Go template, sharing the same `styles.css`).

The cashier source tree stays in place for ad-hoc markdown rendering — essays, single-page posts, anything that benefits from the section system (`hero`, `dual-pillars`, `built`, `prose`, etc.). Invocation is unchanged:

```bash
./scripts/cashier.sh photo input.json output.md
./scripts/cashier.sh build input.md output.html
./scripts/cashier.sh all <dir>
```

The photo `.jpg` sidecar `cmd/cashier` produces is no longer used by `cmd/web` — thumbnails come out of the `thumbnails` BLOB in Postgres.

## Photo Search — pgvector (`cmd/index`, `cmd/search`)

Indexes photo descriptions from the Postgres library into the `chunks` table (pgvector halfvec(2560) + HNSW index). Search is a single SQL query: `ORDER BY embedding <=> $1 LIMIT k`. Uses LM Studio for embeddings and (optionally) for the LLM verify pass.

Two Go binaries plus a shared `internal/library` package:

- **`internal/library`** — `Photo` struct + `LoadPhoto` (pgx), `BuildDocument` (the indexable text — same Go function used by both indexer and verifier), `Chunk` (character-window splitter), `EmbedTexts` and `LLMComplete` (LM Studio HTTP)
- **`cmd/index`** — reads photos from SQL, chunks each `BuildDocument` output, embeds via LM Studio, INSERTs into `chunks`. Idempotent: skips photos that already have chunks unless `-reindex` is passed.
- **`cmd/search`** — embeds the query, runs the cosine SELECT, optionally pipes candidates through the LLM verify pass (8-way goroutine pool)

**How it works:**

1. `cmd/index` iterates `SELECT name FROM photos`, calls `BuildDocument` per row, splits the result into ~6KB chunks, embeds each chunk via LM Studio, INSERTs `(photo_id, idx, text, embedding)` rows
2. Search embeds the query the same way, then aggregates per-photo via `SELECT name, MAX(1 - (embedding <=> $1)) AS similarity FROM chunks JOIN photos ... GROUP BY name ORDER BY similarity DESC LIMIT k`
3. Optional `-verify` runs an LLM yes/no relevance check on each candidate's `BuildDocument` text — same text the indexer embedded, so retrieval and verification stay coherent

**Prerequisites — models loaded in LM Studio:**

Default setup uses Qwen3-VL 8B for vision description plus the embedding model. The verify pass uses Ministral 3B by default — bump to a stronger model via `SEARCH_MODEL` for finer-grained subject judgment.

```bash
# Vision description model
lms load qwen/qwen3-vl-8b

# Embedding model for vector search (2560-dim, GGUF quantized)
lms load text-embedding-qwen3-embedding-4b

# Default verify model (small + fast)
lms load mistralai/ministral-3-3b --context-length 32000 --parallel 8

# Optional: 24B for higher-precision verify
lms load mistralai/devstral-small-2-2512 --context-length 32000 --parallel 4
```

**Usage:**

```bash
# Index every photo currently in the library
./scripts/index.sh

# Re-index (TRUNCATE chunks then re-embed all photos)
./scripts/index.sh -reindex

# Override library DSN
./scripts/index.sh -dsn postgres:///other_db

# Query — pure vector similarity, top 30 by default
./scripts/search.sh "bedroom photos with warm light"

# Retrieval only — top-500, cosine ≥ 0.5, no LLM synthesis
./scripts/search.sh -retrieve "shallow depth of field"

# Strict retrieval — alias for -retrieve
./scripts/search.sh -precise "indoor scenes with warm light"

# Verify: vector retrieval + LLM yes/no check per candidate, keep only YES
./scripts/search.sh -retrieve -verify "April photos with trees"

# Use a different model for the verify pass
SEARCH_MODEL="mistralai/devstral-small-2-2512" ./scripts/search.sh -retrieve -verify "..."
```

**Output flags:**

| Flag | Description |
|------|-------------|
| *(default)* | Top-30 vector retrieval, no LLM |
| `-retrieve` | Top-500 vector retrieval, cosine ≥ 0.5, no LLM (used by cmd/web; output parses cleanly via the `[N] name` regex) |
| `-precise` | Same as `-retrieve` (kept for parity with the old Python tool's `--precise`) |
| `-verify` | Composes with `-retrieve`/`-precise`: runs an LLM yes/no relevance check on each candidate's `BuildDocument` text in an 8-way goroutine pool, keeps only YES matches |

**Environment variables:**

| Variable | Default | Description |
|----------|---------|-------------|
| `LIBRARY_DSN` | `postgres:///ragotogar` | Postgres connection string |
| `TEXT_ENDPOINT` | falls back to `LM_STUDIO_BASE` | OpenAI-compatible URL for the verify-pass text model |
| `EMBED_ENDPOINT` | falls back to `LM_STUDIO_BASE` | OpenAI-compatible URL for the embedding model |
| `LM_STUDIO_BASE` | `http://localhost:1234` | Legacy single-endpoint fallback when neither of the above is set |
| `SEARCH_MODEL` | `mistralai/ministral-3-3b` | LLM for the verify pass |
| `EMBED_MODEL` | `text-embedding-qwen3-embedding-4b` | 2560-dim embedding model — changing it requires re-indexing |

**Key details:**

- Documents combine EXIF metadata, camera settings, and the full visual description into a single text for indexing — vector captures "X100VI", "f/2", "ISO 3200" alongside visual phrases like "bedroom" and "paisley duvet"
- Chunking is a simple character-window splitter (~6KB per chunk with 400-byte overlap). Most photo descriptions fit in a single chunk; only long-form descriptions spill over.
- HNSW index on `embedding halfvec_cosine_ops` — cosine is the metric, `<=>` is the distance operator. halfvec is required because the `vector` type's HNSW caps at 2000 dims; halfvec at 4000.
- Re-indexing is incremental by default (skips photos that already have any chunks); use `-reindex` to TRUNCATE and rebuild
- Per-photo similarity = `MAX(1 - (embedding <=> $1))` over the photo's chunks (best chunk wins) so the same photo doesn't appear multiple times in results
- Embedding model must be `text-embedding-qwen3-embedding-4b` (2560-dim); changing it requires re-indexing
- **Requirements:** Postgres + pgvector (`./scripts/bootstrap.sh`), LM Studio with an embedding model loaded, a populated library (run `cmd/describe` first)

## Web Server (`cmd/web`)

Browser UI sitting on top of the Postgres library. Type a query, get a grid of matching photo thumbnails; click a thumbnail to open the full per-photo page rendered on-demand from SQL via Go's `html/template`. Thumbnails stream from the `thumbnails` BLOB column.

**Usage:**

```bash
./scripts/web.sh                          # default: :8080, library at postgres:///ragotogar
./scripts/web.sh -dsn postgres:///other_db
./scripts/web.sh -addr :9000              # different port
```

Then open `http://localhost:8080`.

**Options:**

| Flag | Default | Description |
|------|---------|-------------|
| `-addr` | `:8080` | Listen address |
| `-dsn` | `postgres:///ragotogar` | Postgres library DSN (overrides `LIBRARY_DSN` env) |
| `-repo` | `.` | Repo root (where `scripts/search.sh` and `styles.css` live) |

**Routes:**

| Route | Behavior |
|-------|----------|
| `GET /` | Search box + a four-pill mode toggle + result grid |
| `GET /?q=<query>&mode=<mode>` | Shells out to `./scripts/search.sh -retrieve [-verify] "<query>"`, parses the file list, validates each name against `photos.name`, renders thumbnails |
| `GET /photos/<name>` | HTML page rendered from a Go template against the photos / exif / descriptions / inference tables. Uses the cashier design system (hero / dual-pillars / built photo-meta sections). |
| `GET /photos/<name>.jpg` | Streams the thumbnail BLOB from `thumbnails.bytes` with `Content-Type: image/jpeg` and `Cache-Control: max-age=86400` |
| `GET /styles.css` | Serves the cashier design system from the repo root |

**Mode toggle:**

| Pill | cmd/search invocation | Behavior |
|------|------------------------|----------|
| `vector` | `-retrieve` | Pure vector similarity (cosine ≥ 0.5, top 500). **Default and recommended** — sub-second on small corpora. |
| `naive-verify` | `-retrieve -verify` | Vector retrieval + an LLM yes/no check on each candidate (text pulled from SQL). ~1–6s per query. Use for tighter precision when "red truck" returns too much red and too much truck-shaped. |
| `graph` | `-retrieve` | (legacy LightRAG concept; no graph backend after Phase 2 — same as vector) |
| `hybrid` | `-retrieve` | (legacy LightRAG concept; same as vector) |

All pills use `--retrieve` (vector retrieval, no LLM synthesis). Clicking a pill auto-submits the form. Results whose name isn't in `photos` are silently skipped (e.g. when chunks reference a basename that's since been deleted from the library).

The verify pass (both verify-mode pills above) consults `verify_cache` before each LLM call. Hit rate is shown directly above the result grid — `verify: 30 candidates · 18 cached · 12 LLM · 60% hit` — so you can watch the rate climb as you iterate on a query. Cache rows for a photo are silently invalidated when the photo is re-described (`verified_at > inference.described_at` freshness check).

**Requirements:** A populated `chunks` table (see [Photo Search](#photo-search--pgvector-tools)) and a populated library (run `cmd/describe` first).

## Tests

Run the full test suite (all Go and bash tests):

```bash
./test.sh
```

Or run individually:

```bash
# Go tests — sub-modules (each has its own go.mod)
cd cmd/organize  && go test -v ./...
cd cmd/describe  && go test -v ./...   # parsers, schema, insert roundtrip, FTS, cascade

# Go tests — root module (cmd/cashier and cmd/web)
go test -v ./cmd/cashier/...
go test -v ./cmd/web/...               # template render, BLOB stream, search-output parse, helpers

# Individual bash test scripts
./scripts/clone_test.sh
./scripts/exif_fix_test.sh
./scripts/flatten_test.sh
./scripts/organize_test.sh
./scripts/sync_to_nas_test.sh
```

`test.sh` auto-discovers all `cmd/*/go.mod` Go sub-modules, the root module (`cmd/cashier`, `cmd/web`), and all `scripts/*_test.sh` bash tests.

**Go organizer test details:**

1. **Fixture generation** — `TestOrganize` generates 1000+ empty files in a temp directory, simulating output from 10 different cameras (Sony, Canon, Fuji, Nikon, Blackmagic, DJI, GoPro, Phase One, Panasonic) with a mix of media extensions, sidecar files, uppercase extensions, orphan sidecars, files with no extension, and unknown extensions
2. **Immutable copy** — Fixtures are copied to a separate working directory before the organizer runs. The original fixture directory is verified unchanged both before and after the organize step
3. **Validation** — After organizing, the test checks:
   - Every media file is in the correct `TypeFolder/DateFolder/` path
   - Every sidecar with a parent is in the same directory as its parent
   - Orphan XMP sidecars still exist (left in place)
   - Orphan XML sidecars ended up in `MP4/<date>/`
   - No media files remain in the root directory
   - No files were lost (total count is preserved)
   - No files ended up in the wrong type folder

**Caveats:**

- **All test files share the same birth time** — macOS birth time is set at file creation and cannot be changed via `os.Chtimes` or any standard Go API. Since all fixtures are created during the same test run, they all get the same birth date. This means the date-grouping pass puts everything into a single date folder. The test reads the actual birth time to determine the expected folder name rather than assuming a specific date.
- **No multi-date coverage in integration test** — Because of the birth time limitation above, the test cannot verify that files from different days end up in different date folders. The `TestFormatDate` and `TestOrdinalSuffix` unit tests cover the date formatting logic independently.
- **macOS only** — The `fileTime` function uses `syscall.Stat_t.Birthtimespec` / `Mtimespec` which are macOS-specific. Tests will not compile on Linux.

## Shell Scripts

### Shared Config (`scripts/.files.env`)

All scripts and the Go organizer share `scripts/.files.env` as the single source of truth for directory names, extension mappings, and exclude patterns:

```bash
PHOTO_DIRS=("JPEG" "HIF" "RAW")
VIDEO_DIRS=("MOV" "MP4" "BRAW" "NEV" "NDF")
AUDIO_DIRS=("AUDIO")

# Extension-to-type-folder mappings (used by Go organizer)
JPEG_EXTS=("jpg" "jpeg")
HIF_EXTS=("hif" "heif" "heic")
RAW_EXTS=("raf" "arw" "nef" "cr2" "cr3" "dng" "orf" "rw2" "pef")
MOV_EXTS=("mov")
MP4_EXTS=("mp4")
BRAW_EXTS=("braw")
NEV_EXTS=("nev")
NDF_EXTS=("ndf")
AUDIO_EXTS=("wav")
SIDECAR_EXTS=("dxo" "dop" "pp3" "xml" "aac" "lrf" "mp3")

# Derived: all photo/image extensions (used by exif_fix.sh)
PHOTO_EXTS=("${JPEG_EXTS[@]}" "${HIF_EXTS[@]}" "${RAW_EXTS[@]}")
MACOS_EXCLUDES=("._*" ".DS_Store")
```

The Go organizer reads this file via the `-config` flag (passed automatically by `organize.sh`). Each `FOO_EXTS` array maps its extensions to a `FOO` type folder. `SIDECAR_EXTS` defines the sidecar file set.

### `scripts/organize.sh` — Go Organizer Wrapper

Convenience wrapper that runs the Go media organizer. Passes all arguments through.

```bash
./scripts/organize.sh /path/to/directory
./scripts/organize.sh -mtime /path/to/directory
```

### `scripts/bootstrap.sh` — One-time Setup

Installs Postgres + pgvector via Homebrew, starts the cluster, creates the `ragotogar` database, loads the vector extension, and applies the schema via `cmd/describe -init-only`. Idempotent. Run after a fresh checkout (or to verify the local Postgres is in good shape).

```bash
./scripts/bootstrap.sh
LIBRARY_DB_NAME=alt ./scripts/bootstrap.sh   # different DB name
```

### `scripts/dir_photos.sh` — Full Directory Pipeline

Describes every photo in a directory; everything (EXIF, parsed fields, full description, thumbnail BLOB, model + timing) lands in the Postgres library. Run `./scripts/index.sh` afterward to embed the descriptions into pgvector and `./scripts/web.sh` to browse.

```bash
./scripts/dir_photos.sh ~/X100VI/JPEG/April2026
./scripts/dir_photos.sh ~/X100VI/JPEG/April2026 -model mistralai/ministral-3-3b
./scripts/dir_photos.sh ~/X100VI/JPEG/April2026 -force   # re-describe everything
```

Arguments: `<photo_dir> [photo_describe flags...]`. All `photo_describe.sh` flags pass through (`-db`, `-force`, `-model`, `-dry-run`, `-retries`, `-preview-workers`, `-inference-workers`).

### `scripts/cashier.sh` — Markdown Renderer Wrapper

Convenience wrapper around `go run ./cmd/cashier`. Not part of the photo pipeline — see [Cashier](#cashier-cmdcashier).

```bash
./scripts/cashier.sh photo input.json output.md
./scripts/cashier.sh build input.md output.html
./scripts/cashier.sh all <dir>
```

### `scripts/web.sh` — Web Server

Runs `cmd/web` with the repo root passed in for `scripts/search.sh` and `styles.css` resolution. Defaults to `:8080` and the canonical library at `postgres:///ragotogar`.

```bash
./scripts/web.sh                          # default
./scripts/web.sh -dsn postgres:///alt     # different library
./scripts/web.sh -addr :9000              # different port
```

### `scripts/batch_photo_describe.sh` — Describe Across Matching Subdirectories

Wraps `photo_describe.sh` so a basename prefix expands to every sibling directory matching that prefix and runs them sequentially with shared flags. Useful when a month contains many date subfolders (e.g. `March21st2026`, `March22nd2026`, …).

```bash
# Describe every directory under /Volumes/T9/X100VI/JPEG matching "March*"
./scripts/batch_photo_describe.sh /Volumes/T9/X100VI/JPEG/March

# Custom library path applies to every matched directory
./scripts/batch_photo_describe.sh -db /tmp/march.db /Volumes/T9/X100VI/JPEG/March
```

All `photo_describe.sh` flags (`-db`, `-force`, `-model`, `-dry-run`, `-retries`) pass through. The last positional argument must be `<parent>/<prefix>`.

### `scripts/clone.sh` — NAS Sync (rclone)

Syncs camera directories to a mounted NAS volume using rclone with parallel transfers. Supports month and year filtering to selectively sync date folders (e.g. `March3rd2024`).

```bash
# Sync everything
./scripts/clone.sh /Volumes/Organized /Volumes/NAS/Media

# Parallel transfers, no videos
./scripts/clone.sh -t 4 --no-videos /Volumes/Organized /Volumes/NAS/Media

# March onward, all years
./scripts/clone.sh -m Mar /Volumes/Organized /Volumes/NAS/Media

# Only February and April of 2025
./scripts/clone.sh -m "Feb,Apr" -y 2025 /Volumes/Organized /Volumes/NAS/Media

# All of 2024
./scripts/clone.sh -y 2024 /Volumes/Organized /Volumes/NAS/Media

# June onward, 2025 only, no videos, 4 transfers
./scripts/clone.sh -m Jun -y 2025 -t 4 --no-videos /Volumes/Organized /Volumes/NAS/Media
```

**Options:**

| Flag | Description |
|------|-------------|
| `-t NUM` | Number of parallel transfers (default: 2) |
| `--no-videos` | Exclude video directories (MOV, MP4, BRAW, NEV, NDF) |
| `-m MONTH` | Month filter — single month (e.g. `Mar`) syncs from that month through December; comma-delimited (e.g. `"Feb,Apr,Jun"`) syncs only those months. Accepts full names or abbreviations. |
| `-y YEAR` | Year filter — only sync date folders matching that year. Without `-m`, syncs all months of that year. |

**Month/year filter behavior:**

- `-m Mar` — March through December, all years
- `-m "Feb,Jun"` — only February and June, all years
- `-y 2024` — all months, 2024 only
- `-m Mar -y 2025` — March through December, 2025 only
- No flags — syncs everything (including top-level files)
- When a date filter is active, top-level files (outside date folders) are skipped

**Behavior:**

- New files are copied over
- Existing files are only overwritten if the source is newer (`--update`)
- Destination directories are created automatically
- Excludes `._*` and `.DS_Store` files

**Requirements:** macOS, [rclone](https://rclone.org/), mounted NAS volume

### `scripts/flatten.sh` — Directory Flattener

Pulls all files from nested subdirectories into a single target directory, then removes empty subdirectories. Useful as a pre-organize step when importing from cameras with nested folder structures.

```bash
./scripts/flatten.sh /Volumes/CameraCard/DCIM
```

- Handles name collisions by appending a counter suffix (`file_1.jpg`, `file_2.jpg`)
- Skips macOS `._` resource fork files

### `scripts/exif_fix.sh` — EXIF Date Fix

Sets file created and modified dates from EXIF `DateTimeOriginal`. Recursively processes all supported image formats. Useful when files have been bulk-copied and lost their original filesystem dates.

```bash
./scripts/exif_fix.sh /Volumes/CameraCards/organized
```

**Supported extensions:** jpg, jpeg, hif, heif, heic, raf, arw, nef, cr2, cr3, dng, orf, rw2, pef

**Requirements:** [exiftool](https://exiftool.org/) (`brew install exiftool`)

### `scripts/sync_to_nas.sh` — NAS Sync (rsync)

Syncs camera directories to a mounted NAS volume using rsync. Each subdirectory in the source (one per camera) is synced to a matching folder on the NAS.

```bash
./scripts/sync_to_nas.sh /Volumes/CameraCards /Volumes/NAS/Media
```

**Behavior:**

- New files are copied over
- Existing files are only overwritten if the source is newer (`--update`)
- Destination directories are created automatically
- Preserves timestamps, permissions, and directory structure (`-a`)

**Requirements:** macOS, mounted NAS volume
