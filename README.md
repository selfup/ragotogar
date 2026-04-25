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
| 2. **Describe** | Send each photo to a vision LLM, get structured JSON with EXIF + visual description | `scripts/photo_describe.sh` |
| 3. **Render** | Convert JSON descriptions → markdown → self-contained HTML photo pages | `scripts/dir_photos.sh` |
| 4. **Index** | Extract entities/relationships into a knowledge graph and embed descriptions as vectors | `tools/index_and_vectorize.sh` |
| 5. **Search** | Query the graph with natural language, get synthesized answers or file lists | `tools/search.sh` |

Steps are independent — you can run search without ever organizing, or describe without syncing to a NAS. The typical full flow is 1 → 2 → 3 → 4 → 5.

## Components

| Component | Location | Description |
|-----------|----------|-------------|
| Go Media Organizer | `cmd/organize` | Parallel file organizer: sorts media into type/date folders, reunites sidecars. macOS-only. |
| Photo Describer | `cmd/describe` | Vision LLM descriptions + EXIF metadata → structured JSON |
| Photo Renderer | `cmd/cashier` | JSON → markdown → self-contained HTML photo pages |
| NAS Sync | `scripts/clone.sh` | rclone-based sync with month/year filtering and `--no-videos` |
| GraphRAG Search | `tools/` | LightRAG knowledge graph for semantic and graph-based photo search |

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

Extracts EXIF metadata and generates LLM-powered visual descriptions of photos using LM Studio's vision API. Outputs structured JSON with both machine-parseable fields and a full-text description suitable for RAG indexing.

**Usage:**

```bash
./scripts/photo_describe.sh /path/to/photos

# Custom output directory
./scripts/photo_describe.sh -output ./descriptions /path/to/photos

# Preview which files would be processed
./scripts/photo_describe.sh -dry-run /path/to/photos

# Run a Ministral OCR pass for text-heavy scenes (see STRATEGIES.md)
./scripts/photo_describe.sh -output ./descriptions/ministral-ocr -model mistralai/ministral-3-3b /path/to/photos

# More retry attempts for flaky models
./scripts/photo_describe.sh -retries 5 /path/to/photos
```

**Options:**

| Flag | Description |
|------|-------------|
| `-output DIR` | Output directory for .json files (default: `<input_dir>/descriptions`) |
| `-model NAME` | LM Studio model name (default: `qwen/qwen3-vl-8b` or `LM_MODEL` env) |
| `-dry-run` | List files without calling the LLM |
| `-retries N` | Max retry attempts per image on API failure (default: 3) |

**Environment variables:**

| Variable | Default | Description |
|----------|---------|-------------|
| `LM_STUDIO_BASE` | `http://localhost:1234` | LM Studio API endpoint |
| `LM_MODEL` | `qwen/qwen3-vl-8b` | Vision model name (see `STRATEGIES.md` for model comparison) |
| `RESIZE_PX` | `1024` | Longest edge resize for preview |
| `JPEG_QUALITY` | `85` | JPEG quality for resized preview |

**Output format:**

Each photo produces a JSON file named `<date>_<camera>_<filename>.json`:

```json
{
  "name": "20250928_X100VI_DSCF1516",
  "file": "DSCF1516.JPG",
  "path": "/path/to/DSCF1516.JPG",
  "preview_ms": 180,
  "preview": "180ms",
  "inference_ms": 24251,
  "inference": "24.252s",
  "metadata": {
    "file_name": "DSCF1516.JPG",
    "date_time_original": "2025:09:28 16:38:17",
    "make": "FUJIFILM",
    "model": "X100VI",
    "focal_length": "23.0 mm",
    "f_number": "2",
    "exposure_time": "1/38",
    "iso": "3200",
    "..."
  },
  "fields": {
    "subject": "A bed covered in a paisley duvet...",
    "setting": "A bedroom with beige walls...",
    "light": "Dim, warm, from window on left...",
    "colors": "Rust orange, muted blue, cream...",
    "composition": "Low angle, shallow depth of field..."
  },
  "description": "**Subject:** A bed covered in..."
}
```

- `fields` — structured data for direct filtering/querying
- `description` — full text for LightRAG or similar RAG indexing
- `metadata` — EXIF data parsed via `exiftool -json`
- Output filenames include date + camera model to avoid collisions across cameras

**Key details:**

- RAW files (RAF, ARW, NEF, etc.) use the embedded JPEG preview via `exiftool -b -PreviewImage`, avoiding the need for darktable or rawtherapee
- Non-RAW files are resized to 1024px previews via ImageMagick (configurable)
- Skips `._` AppleDouble files and `.DS_Store`
- Exponential backoff with jitter on API failures
- Unique session ID per request to prevent LM Studio KV cache reuse
- Strips `<think>` blocks from reasoning models; detects when model exhausts tokens on reasoning with no content
- Skips already-processed files (re-run safe)
- **Requirements:** [exiftool](https://exiftool.org/), [ImageMagick](https://imagemagick.org/), LM Studio running with a vision model loaded

## Photo Renderer (`cmd/cashier`)

Converts photo description JSON files into styled, self-contained HTML pages. Takes the output of `cmd/describe` and produces one `.md` and one `.html` per photo — all styles are inlined, no external dependencies at view time.

**Full directory pipeline (describe + render):**

```bash
# Describe all photos in a directory and render to HTML in one command
./scripts/dir_photos.sh ~/X100VI/JPEG/April2026

# Custom output directory
./scripts/dir_photos.sh ~/X100VI/JPEG/April2026 describe_april

# With photo_describe flags (model, retries, etc.)
./scripts/dir_photos.sh ~/X100VI/JPEG/April2026 describe_april -model mistralai/ministral-3b
```

**Render only (JSON already exists):**

```bash
# Single photo: JSON → md + html, then open
./scripts/photo.sh describe_april/20260417_X100VI_DSCF1781.json \
                   describe_april/20260417_X100VI_DSCF1781.md \
                   describe_april/20260417_X100VI_DSCF1781.html

# Batch: all JSON → md + html, 8 workers
go run ./cmd/cashier all describe_april

# Or as separate passes:
go run ./cmd/cashier photo-all describe_april   # JSON → md
go run ./cmd/cashier build-all describe_april   # md → html
```

**Single file:**

```bash
go run ./cmd/cashier photo input.json output.md
go run ./cmd/cashier build input.md output.html
```

**Options:**

| Flag | Default | Description |
|------|---------|-------------|
| `-workers N` | `8` | Parallel workers for batch commands |

**Output:** Each photo gets a `<date>_<camera>_<filename>.html` with the photo embedded as a base64 JPEG alongside structured sections: visual analysis (subject, setting, light, colors, composition), full EXIF metadata table, and a closing summary.

**Requirements:** [ImageMagick](https://imagemagick.org/) (for embedding the photo into the HTML), Go 1.26+

## Photo Search — GraphRAG (`tools/`)

Indexes photo description JSONs into a knowledge graph using [LightRAG](https://github.com/HKUDS/LightRAG), enabling semantic and graph-based search across your photo library. Uses LM Studio for both LLM (entity/relationship extraction) and embeddings.

Split into three modules with separate concerns:

- **`rag_common.py`** — shared config (models, index dir), LLM/embedding functions, RAG initialization
- **`index_and_vectorize.py`** — entity extraction + vector embedding via `ainsert()`
- **`search.py`** — query-only, searches the built index

**How it works:**

1. Each photo's description text is chunked and sent to the LLM for entity extraction (objects, rooms, cameras, settings, colors, etc.)
2. Extracted entities and relationships are stored in a knowledge graph (NetworkX + GraphML)
3. Description text is also embedded via a local embedding model for vector search
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
# Index photo descriptions
./tools/index_and_vectorize.sh /path/to/description_jsons

# Re-index (clear existing graph and rebuild)
./tools/index_and_vectorize.sh --reindex /path/to/description_jsons

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
| `--retrieve` | Retrieval only — strict matching (cosine ≥ 0.5, naive mode), returns matched file list with no LLM synthesis |
| `--precise` | Strict retrieval (cosine ≥ 0.5, naive mode) then synthesize over only exact matches. Best with `SEARCH_MODEL` override for large result sets. See [`STRATEGIES.md`](STRATEGIES.md). |

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
- `index_and_vectorize.py` globs `**/*.json` recursively, so pointing it at a parent `descriptions/` directory picks up subdirectories automatically (e.g. `descriptions/ministral-ocr/*.json` alongside the main descriptions — LightRAG entity-merge deduplicates across both)
- All documents are batched into a single `ainsert()` call so LightRAG processes chunks in parallel across 8 concurrent LLM workers
- Embedding model must be `nomic-embed-text-v1.5` (768-dim) — changing the embedding model requires re-indexing
- Index is stored in `tools/.rag_index/` (gitignored)
- **Requirements:** Python 3.10+, LM Studio with an LLM and embedding model loaded

## Tests

Run the full test suite (all Go and bash tests):

```bash
./test.sh
```

Or run individually:

```bash
# Go tests — sub-modules
cd cmd/organize && go test -v ./...

# Go tests — root module (cmd/cashier)
go test -v ./cmd/cashier/...

# Individual bash test scripts
./scripts/clone_test.sh
./scripts/exif_fix_test.sh
./scripts/flatten_test.sh
./scripts/organize_test.sh
./scripts/sync_to_nas_test.sh
```

`test.sh` auto-discovers all `cmd/*/go.mod` Go sub-modules, the root module (`cmd/cashier`), and all `scripts/*_test.sh` bash tests.

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

Runs the complete pipeline on a directory of photos: describe → render.

```bash
./scripts/dir_photos.sh ~/X100VI/JPEG/April2026
./scripts/dir_photos.sh ~/X100VI/JPEG/April2026 describe_april
./scripts/dir_photos.sh ~/X100VI/JPEG/April2026 describe_april -model mistralai/ministral-3b
```

Arguments: `<photo_dir> [output_dir] [photo_describe flags...]`. Output directory defaults to `describe_output`.

### `scripts/photo.sh` — Single Photo Render

Converts a single description JSON to markdown and HTML, then opens the result.

```bash
./scripts/photo.sh describe_output/20260417_X100VI_DSCF1781.json \
                   describe_output/20260417_X100VI_DSCF1781.md \
                   describe_output/20260417_X100VI_DSCF1781.html
```

### `scripts/cashier.sh` — Photo Renderer Wrapper

Convenience wrapper around `go run ./cmd/cashier`. Passes all arguments through.

```bash
./scripts/cashier.sh photo input.json output.md
./scripts/cashier.sh build input.md output.html
./scripts/cashier.sh all describe_output/
./scripts/cashier.sh photo-all describe_output/
./scripts/cashier.sh build-all describe_output/
```

### `scripts/batch_photo_describe.sh` — Describe Across Matching Subdirectories

Wraps `photo_describe.sh` so a basename prefix expands to every sibling directory matching that prefix and runs them sequentially with shared flags. Useful when a month contains many date subfolders (e.g. `March21st2026`, `March22nd2026`, …).

```bash
# Describe every directory under /Volumes/T9/X100VI/JPEG matching "March*"
./scripts/batch_photo_describe.sh -output describe_test /Volumes/T9/X100VI/JPEG/March
```

All `photo_describe.sh` flags (`-output`, `-model`, `-dry-run`, `-retries`) pass through. The last positional argument must be `<parent>/<prefix>`.

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
