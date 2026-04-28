# Ragotogar

_Research preview :warning: this project is under heavy discovery work and development_

**RAG Photo Organizer** — a local-LLM photo library with GraphRAG search. *(Yes, it's a palindrome.)*

A collection of utility shell scripts and Go programs to: organize, normalize, describe, render, and search media.

## Requirements

- **macOS** — the organizer uses macOS-specific syscalls for file birth time
- **[LM Studio](https://lmstudio.ai/)** — local LLM inference server (vision + text + embedding models)
- **[exiftool](https://exiftool.org/)** — EXIF metadata extraction (`brew install exiftool`)
- **[ImageMagick](https://imagemagick.org/)** — image resizing for LLM previews and HTML rendering (`brew install imagemagick`)
- **[rclone](https://rclone.org/)** — NAS sync (`brew install rclone`)
- **Go 1.26+** — all pipeline tools are pure Go (`go run`, no build step)
- **Python 3.10+** + **[uv](https://docs.astral.sh/uv/)** — for GraphRAG search tools (`brew install uv`)

## Pipeline

Each step feeds the next:

| Step | What | Script |
|------|------|--------|
| 1. **Organize** | Sort media into type folders (JPEG, RAW, MOV...) and date subfolders | `scripts/organize.sh` |
| 2. **Describe** | Send each photo to a vision LLM, write photo + EXIF + parsed fields + 1024px thumbnail BLOB into `tools/.sql_index/library.db` | `scripts/photo_describe.sh` |
| 3. **Index** | Read each photo from SQL and feed into a LightRAG knowledge graph (entity extraction + vector embedding) | `tools/index_and_vectorize.sh` |
| 4. **Search** | Query the graph with natural language, get synthesized answers or file lists | `tools/search.sh` |
| 5. **Browse** | Web server — search box + thumbnail grid; per-photo pages render on-demand from SQL | `scripts/web.sh` |

Steps are independent — you can run search without ever organizing, or describe without syncing to a NAS. The typical full flow is 1 → 2 → 3 → 4 → 5.

## Components

| Component | Location | Description |
|-----------|----------|-------------|
| Go Media Organizer | `cmd/organize` | Parallel file organizer: sorts media into type/date folders, reunites sidecars. macOS-only. |
| Photo Describer | `cmd/describe` | Vision LLM descriptions + EXIF metadata + thumbnail JPG → SQLite library.db |
| NAS Sync | `scripts/clone.sh` | rclone-based sync with month/year filtering and `--no-videos` |
| GraphRAG Search | `tools/` | LightRAG knowledge graph (reads photos from SQL); semantic + graph search |
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

Extracts EXIF metadata, generates a 1024px thumbnail, and produces an LLM visual description via LM Studio's vision API. All output (typed EXIF columns, parsed `subject/setting/light/colors/composition` fields, full description, thumbnail BLOB, model + timing) writes directly to a single SQLite library at `tools/.sql_index/library.db`. The `photos` table is the source of truth — no JSON sidecars, no MD/HTML files.

**Usage:**

```bash
./scripts/photo_describe.sh /path/to/photos

# Custom library path
./scripts/photo_describe.sh -db /tmp/other.db /path/to/photos

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
| `-db PATH` | SQLite library path (default: `tools/.sql_index/library.db`, resolved from the repo root) |
| `-force` | Re-describe photos already in the DB; UPSERTs across all five tables |
| `-model NAME` | LM Studio model name (default: `qwen/qwen3-vl-8b` or `LM_MODEL` env) |
| `-dry-run` | List files without calling the LLM or touching the DB |
| `-retries N` | Max retry attempts per image on API failure (default: 3) |
| `-preview-workers N` | Parallel ImageMagick/exiftool workers for preview generation (default: 4) |
| `-inference-workers N` | Parallel LLM inference workers (default: 1). Bump to N to use LM Studio's `--parallel N` continuous batching. Vision inference is more memory-intensive than text — start at 2–4 and watch VRAM/error rate before going higher. |

**Environment variables:**

| Variable | Default | Description |
|----------|---------|-------------|
| `LM_STUDIO_BASE` | `http://localhost:1234` | LM Studio API endpoint |
| `LM_MODEL` | `qwen/qwen3-vl-8b` | Vision model name (see `STRATEGIES.md` for model comparison) |
| `RESIZE_PX` | `1024` | Longest edge resize for preview (also the thumbnail BLOB size) |
| `JPEG_QUALITY` | `85` | JPEG quality for resized preview |

**Library schema:**

`cmd/describe` is the schema authority — it applies `CREATE TABLE IF NOT EXISTS` on every run, so the DB is created on first invocation and migrated forward on subsequent ones. Five tables, all keyed on `photos.id` (currently equal to `name` until Phase 5 stable IDs):

| Table | Holds |
|-------|-------|
| `photos` | `id`, `name`, `file_path` (original on disk), `file_basename`, timestamps |
| `exif` | Typed columns from EXIF: `camera_make`, `camera_model`, `lens_model`, `date_taken` (ISO 8601 + decomposed year/month), `focal_length_mm`, `f_number`, `exposure_time_seconds`, `iso`, `exposure_compensation`, `gps_latitude/longitude`, etc. |
| `descriptions` | Parsed `subject / setting / light / colors / composition` plus `full_description` (the raw LLM output) |
| `descriptions_fts` | FTS5 virtual table over `descriptions` with `porter unicode61` tokenization. Triggers keep it in sync. |
| `thumbnails` | 1024px JPG bytes as a BLOB. Generated from the same magick output sent to the vision model — no second resize. |
| `inference` | `model`, `preview_ms`, `inference_ms`, `described_at` |

The `photos.id` column is reserved for the Phase 5 stable hash; it equals `name` today. Photo filenames include date + camera model (`20250928_X100VI_DSCF1516`) to avoid collisions across cameras.

**Key details:**

- RAW files (RAF, ARW, NEF, etc.) use the embedded JPEG preview via `exiftool -b -PreviewImage`, avoiding the need for darktable or rawtherapee
- Non-RAW files are resized to 1024px previews via ImageMagick (configurable)
- The same JPG bytes are base64-encoded for the LLM and stored as the thumbnail BLOB — single magick pass
- Skips `._` AppleDouble files and `.DS_Store`
- Exponential backoff with jitter on API failures
- Unique session ID per request to prevent LM Studio KV cache reuse
- Strips `<think>` blocks from reasoning models; detects when model exhausts tokens on reasoning with no content
- Skip-exists checks `SELECT 1 FROM photos WHERE name = ?` (use `-force` to re-describe)
- All five tables get UPSERTed inside one transaction per photo
- **Requirements:** [exiftool](https://exiftool.org/), [ImageMagick](https://imagemagick.org/), LM Studio running with a vision model loaded

**Ad-hoc SQL:**

```bash
./tools/sql_query.sh "SELECT camera_model, COUNT(*) FROM exif GROUP BY camera_model"
./tools/sql_query.sh -f /path/to/query.sql
```

**Backfill:** if you have an existing library populated by an earlier pipeline (no `thumbnails` / `inference` rows), `./tools/bootstrap_thumbs_inference.sh` re-derives both from `photos.file_path` (magick) and any matching `describe_*/<name>.json` sidecars on disk.

## Cashier (`cmd/cashier`)

A general-purpose markdown → HTML renderer that ships the cashier design system (warm-paper editorial style; see `styles.css`). Used to live in the photo pipeline producing per-photo `.md` / `.html` / `.jpg` sidecars; that role is now in `cmd/web` (per-photo pages render on-demand from SQL via Go template, sharing the same `styles.css`).

The cashier source tree stays in place for ad-hoc markdown rendering — essays, single-page posts, anything that benefits from the section system (`hero`, `dual-pillars`, `built`, `prose`, etc.). Invocation is unchanged:

```bash
./scripts/cashier.sh photo input.json output.md
./scripts/cashier.sh build input.md output.html
./scripts/cashier.sh all <dir>
```

The photo `.jpg` sidecar `cmd/cashier` produces is no longer used by `cmd/web` — thumbnails come out of the `thumbnails` BLOB in `library.db`.

## Photo Search — GraphRAG (`tools/`)

Indexes photo descriptions from `library.db` into a knowledge graph using [LightRAG](https://github.com/HKUDS/LightRAG), enabling semantic and graph-based search across your photo library. Uses LM Studio for both LLM (entity/relationship extraction) and embeddings.

Split into three modules with separate concerns:

- **`rag_common.py`** — shared config (models, paths), LLM/embedding functions, RAG initialization, SQL helpers (`connect_library`, `fetch_photo_dict`, `iter_photo_names`)
- **`index_and_vectorize.py`** — reads photos from SQL, runs entity extraction + vector embedding via `ainsert()`
- **`search.py`** — query-only; verify mode pulls indexable text from SQL too, so retrieval and verification stay coherent

**How it works:**

1. `index_and_vectorize.py` iterates `SELECT name FROM photos`, reshapes each row's typed columns into the dict `build_document()` expects, and feeds the resulting text into LightRAG
2. The LLM extracts entities (objects, rooms, cameras, settings, colors, etc.) into a knowledge graph (NetworkX + GraphML)
3. The same text is embedded via a local embedding model for vector search
4. Queries combine vector similarity with graph traversal for comprehensive results

**Setup:**

```bash
./tools/setup.sh              # uv sync — installs dependencies
```

**Prerequisites — models loaded in LM Studio:**

Default setup uses Qwen3-VL 8B for vision description and Ministral 3B for indexing and search, plus the embedding model. See [`STRATEGIES.md`](STRATEGIES.md) for the model comparison and sizing rationale.

```bash
# Vision description model (best accuracy, 6.6s/photo)
lms load qwen/qwen3-vl-8b

# Indexing and search LLM (fast, GGUF with parallel batching)
lms load mistralai/ministral-3-3b --context-length 65536 --parallel 8

# Embedding model for vector search (768-dim)
lms load text-embedding-nomic-embed-text-v1.5

# Optional: 24B model for multi-document synthesis queries (global/hybrid modes over many chunks)
# Only load if you plan to override SEARCH_MODEL for synthesis-heavy queries.
lms load mistralai/devstral-small-2-2512 --context-length 32000 --parallel 4
```

> **Context length trap:** when loading with `--parallel N`, LM Studio hard-partitions context across slots (each slot gets `context / N` tokens) unless **Unified KV** is enabled in the GUI. LightRAG's entity-extraction prompts run ~4100–5100 tokens, so `--context-length 32000 --parallel 8` gives only 4000/slot and will fail sporadically with 400 errors. Oversize `--context-length` or enable Unified KV. See [`STRATEGIES.md`](STRATEGIES.md) for details.

**Usage:**

```bash
# Index every photo currently in library.db
./tools/index_and_vectorize.sh

# Re-index (clear existing graph and rebuild)
./tools/index_and_vectorize.sh --reindex

# Override library path
./tools/index_and_vectorize.sh --db /tmp/other.db

# Query (hybrid mode — combines local graph + global summaries)
./tools/search.sh "bedroom photos with warm light"

# Query with specific mode
./tools/search.sh --mode naive "shallow depth of field"     # pure vector search (fastest)
./tools/search.sh --mode local "what objects are on the desk" # graph neighborhood
./tools/search.sh --mode global "summarize all indoor scenes" # full graph reasoning
./tools/search.sh --mode hybrid "what cameras were used"      # local + global combined

# Synthesis + list all retrieved source files
./tools/search.sh --sources --mode global "summarize all indoor scenes"

# Retrieval only — strict matching, no LLM synthesis, just the file list
./tools/search.sh --retrieve "airplanes"

# Strict retrieval then synthesize over only exact matches
./tools/search.sh --precise "what is the most common framing I use indoors"

# Use a different model for search
SEARCH_MODEL="mistralai/devstral-small-2-2512" ./tools/search.sh --mode global "roadtrip in winter"

# Precise mode with devstral for exhaustive multi-photo analysis
SEARCH_MODEL="mistralai/devstral-small-2-2512" ./tools/search.sh --precise "analyze the framing of every indoor photo"
```

**Query modes:**

| Mode | Description |
|------|-------------|
| `naive` | Pure vector similarity search on text chunks, no knowledge graph (fastest) |
| `local` | Retrieves relevant entities, then uses their graph neighborhood as context |
| `global` | Uses community summaries from the full knowledge graph for broad thematic answers |
| `hybrid` | Combines local + global for the most comprehensive results (default) |

**Output flags** (mutually exclusive):

| Flag | Description |
|------|-------------|
| *(default)* | Synthesis only — LLM answer from retrieved context |
| `--sources` | Synthesis + full list of all retrieved source files |
| `--retrieve` | Retrieval only — strict matching (cosine ≥ 0.5), returns matched file list with no LLM synthesis. Honors `--mode` (default: `hybrid`). |
| `--precise` | Strict retrieval (cosine ≥ 0.5) then synthesize over only exact matches. Honors `--mode`. Best with `SEARCH_MODEL` override for large result sets. See [`STRATEGIES.md`](STRATEGIES.md). |
| `--verify` | Composes with `--retrieve`: runs an LLM yes/no relevance check on each candidate, keeps only YES matches. Pulls the same indexable text from `library.db` that the indexer used, so retrieval and verification stay coherent. Override the library path with `--db <path>` if needed. |

**Environment variables:**

| Variable | Default | Description |
|----------|---------|-------------|
| `LM_STUDIO_BASE` | `http://localhost:1234` | LM Studio API endpoint |
| `INDEX_MODEL` | `mistralai/ministral-3-3b` | LLM for entity extraction during indexing |
| `SEARCH_MODEL` | `mistralai/ministral-3-3b` | LLM for query synthesis |
| `EMBED_MODEL` | `text-embedding-nomic-embed-text-v1.5` | Embedding model for vector search |
| `COSINE_THRESHOLD` | `0.2` | Cosine similarity threshold for vector retrieval |
| `CHUNK_TOP_K` | `20` | Max chunks returned by retrieval |

> **Model sizing notes:** `INDEX_MODEL` is a text-only task — 3B is fast with GGUF parallel batching and validated equivalent to devstral on entity density (~9 entities/photo). `SEARCH_MODEL` at 3B is adequate for `naive`/`local` and most single-photo answers, but for `global`/`hybrid`/`--precise` multi-document synthesis, override to 24B: `SEARCH_MODEL="mistralai/devstral-small-2-2512" ./tools/search.sh --precise "..."` — small models fixate on a few chunks; 24B cites across many. `--retrieve` and `--precise` override `COSINE_THRESHOLD` to 0.5 and `CHUNK_TOP_K` to 500 (effectively uncapped). See [`STRATEGIES.md`](STRATEGIES.md) for validated thresholds and model comparisons.

**Key details:**

- Documents combine EXIF metadata, camera settings, and the full visual description into a single text for indexing — the graph captures entities like "X100VI", "f/2", "ISO 3200" alongside visual entities like "bedroom" and "paisley duvet"
- Three-slot model architecture: Qwen3-VL 8B for description (vision); Ministral 3B GGUF for index entity extraction and query synthesis; devstral 24B GGUF for multi-document synthesis override. See [`STRATEGIES.md`](STRATEGIES.md) for the five-model comparison and sizing evidence.
- LLM calls use `max_tokens: -1` to let LM Studio use the full context window
- `index_and_vectorize.py` reads every photo from `library.db` via a JOIN across photos / exif / descriptions; new photos picked up on the next run
- All documents are batched into a single `ainsert()` call so LightRAG processes chunks in parallel across 8 concurrent LLM workers
- Embedding model must be `nomic-embed-text-v1.5` (768-dim) — changing the embedding model requires re-indexing
- LightRAG index is stored in `tools/.rag_index/` (gitignored), separate from the SQL library at `tools/.sql_index/library.db`
- **Requirements:** Python 3.10+, LM Studio with an LLM and embedding model loaded, a populated `library.db` (run `cmd/describe` first)

## Web Server (`cmd/web`)

Browser UI sitting on top of `library.db`. Type a query, get a grid of matching photo thumbnails; click a thumbnail to open the full per-photo page rendered on-demand from SQL via Go's `html/template`. Thumbnails stream from the `thumbnails` BLOB column.

**Usage:**

```bash
./scripts/web.sh                          # default: :8080, library at tools/.sql_index/library.db
./scripts/web.sh -db /tmp/other.db        # different library
./scripts/web.sh -addr :9000              # different port
```

Then open `http://localhost:8080`.

**Options:**

| Flag | Default | Description |
|------|---------|-------------|
| `-addr` | `:8080` | Listen address |
| `-db` | `tools/.sql_index/library.db` | SQLite library path |
| `-repo` | `.` | Repo root (where `tools/search.sh` and `styles.css` live) |

**Routes:**

| Route | Behavior |
|-------|----------|
| `GET /` | Search box + a four-pill mode toggle + result grid |
| `GET /?q=<query>&mode=<mode>` | Shells out to `./tools/search.sh --retrieve --mode <mode> "<query>"`, parses the file list, validates each name against `photos.name`, renders thumbnails |
| `GET /photos/<name>` | HTML page rendered from a Go template against the photos / exif / descriptions / inference tables. Uses the cashier design system (hero / dual-pillars / built photo-meta sections). |
| `GET /photos/<name>.jpg` | Streams the thumbnail BLOB from `thumbnails.bytes` with `Content-Type: image/jpeg` and `Cache-Control: max-age=86400` |
| `GET /styles.css` | Serves the cashier design system from the repo root |

**Mode toggle:**

| Pill | search.py invocation | Behavior |
|------|----------------------|----------|
| `vector` | `--retrieve --mode naive` | Pure vector similarity. **Default and recommended** — wins on this corpus. <500ms. |
| `naive-verify` | `--retrieve --mode naive --verify` | Vector retrieval + an LLM yes/no check on each candidate (text pulled from SQL). ~3–6s per query. Use when vector returns too many noisy hits and you want a stricter precision filter. |
| `graph` | `--retrieve --mode local` | LLM extracts keywords from the query, then walks the graph from matched entities. ~1–2s. Often underperforms naive on small corpora where entity coverage is thin. |
| `hybrid` | `--retrieve --mode hybrid` | local + global community summaries. Broadest coverage, usually similar to local for direct photo lookup. |

All pills use `--retrieve` (cosine ≥ 0.5, no LLM synthesis). Clicking a pill auto-submits the form. Results whose name isn't in `photos` are silently skipped (e.g. when LightRAG indexed a basename that's since been deleted from the library).

**Requirements:** A built LightRAG index (see [Photo Search](#photo-search--graphrag-tools)) and a populated `library.db` (run `cmd/describe` first).

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

### `scripts/dir_photos.sh` — Full Directory Pipeline

Describes every photo in a directory; everything (EXIF, parsed fields, full description, thumbnail BLOB, model + timing) lands in `tools/.sql_index/library.db`. Run `./tools/index_and_vectorize.sh` afterward to build the LightRAG graph and `./scripts/web.sh` to browse.

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

Runs `cmd/web` with the repo root passed in for `tools/search.sh` and `styles.css` resolution. Defaults to `:8080` and the canonical library at `tools/.sql_index/library.db`.

```bash
./scripts/web.sh                          # default
./scripts/web.sh -db /tmp/other.db        # different library
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
