# Organize Photos and Sync

A collection of utility shell scripts, go scripts, and python scripts to: organize, normalize, describe, and search media.

This project is a photo/media organization toolkit with four main components:                                                                                                 
               
1. Go Media Organizer (cmd/organize) — Parallel file organizer: sorts media into type folders, date subfolders, and reunites orphaned sidecars. macOS-only.

2. Photo Describer (cmd/describe) — Sends photos to a local LM Studio vision model, outputs structured JSON with EXIF metadata and visual descriptions for RAG indexing.

3. NAS Sync (scripts/clone.sh) — rclone-based sync to and from a mounted NAS with month/year filtering, parallel transfers, and --no-videos support.

4. GraphRAG Search (tools/) — Indexes photo description JSONs into a LightRAG knowledge graph for semantic and graph-based search across the photo library.

Plus shell scripts for directory flattening, EXIF date fixing, and a shared config (.files.env) as the single source of truth for extension mappings.                  

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

# Use a specific model (e.g. a second loaded instance)
./scripts/photo_describe.sh -model qwen3.5-35b-a3b:2 /path/to/photos

# More retry attempts for flaky models
./scripts/photo_describe.sh -retries 5 /path/to/photos
```

**Options:**

| Flag | Description |
|------|-------------|
| `-output DIR` | Output directory for .json files (default: `<input_dir>/descriptions`) |
| `-model NAME` | LM Studio model name (default: `qwen3.5-35b-a3b` or `LM_MODEL` env) |
| `-dry-run` | List files without calling the LLM |
| `-retries N` | Max retry attempts per image on API failure (default: 3) |

**Environment variables:**

| Variable | Default | Description |
|----------|---------|-------------|
| `LM_STUDIO_BASE` | `http://localhost:1234` | LM Studio API endpoint |
| `LM_MODEL` | `qwen3.5-35b-a3b` | Vision model name |
| `RESIZE_PX` | `1024` | Longest edge resize for preview |
| `JPEG_QUALITY` | `85` | JPEG quality for resized preview |

**Output format:**

Each photo produces a JSON file named `<date>_<camera>_<filename>.json`:

```json
{
  "name": "20250928_X100VI_DSCF1516",
  "file": "DSCF1516.JPG",
  "path": "/path/to/DSCF1516.JPG",
  "duration_ms": 24251,
  "duration": "24.252s",
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
cd tools
./setup.sh                    # creates .venv and installs dependencies
```

**Prerequisites — two models loaded in LM Studio:**

```bash
# LLM for entity extraction
lms load qwen3.5-35b-a3b

# Smaller model for search queries (fast answers)
lms load nvidia/nemotron-3-nano-4b

# Embedding model for vector search
lms load text-embedding-nomic-embed-text-v1.5
```

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

# Use a different model for search
SEARCH_MODEL="qwen3.5-35b-a3b" ./tools/search.sh "warm light"
```

**Query modes:**

| Mode | Description |
|------|-------------|
| `naive` | Pure vector similarity search on text chunks, no knowledge graph (fastest) |
| `local` | Retrieves relevant entities, then uses their graph neighborhood as context |
| `global` | Uses community summaries from the full knowledge graph for broad thematic answers |
| `hybrid` | Combines local + global for the most comprehensive results (default) |

**Environment variables:**

| Variable | Default | Description |
|----------|---------|-------------|
| `LM_STUDIO_BASE` | `http://localhost:1234` | LM Studio API endpoint |
| `INDEX_MODEL` | `qwen3.5-35b-a3b` | LLM for entity extraction during indexing |
| `SEARCH_MODEL` | `nvidia/nemotron-3-nano-4b` | LLM for query keyword extraction and answering |
| `EMBED_MODEL` | `text-embedding-nomic-embed-text-v1.5` | Embedding model for vector search |

**Key details:**

- Documents combine EXIF metadata, camera settings, and the full visual description into a single text for indexing — the graph captures entities like "X100VI", "f/2", "ISO 3200" alongside visual entities like "bedroom" and "paisley duvet"
- Indexing uses a non-reasoning model (devstral) for faster entity extraction; search uses a small model (nemotron) for fast query answering
- LLM calls use `max_tokens: -1` to let LM Studio use the full context window
- `llm_model_max_async=1` ensures sequential LLM calls since LM Studio can't handle concurrent requests to the same model
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
# Go tests
cd cmd/organize && go test -v ./...

# Individual bash test scripts
./scripts/clone_test.sh
./scripts/exif_fix_test.sh
./scripts/flatten_test.sh
./scripts/organize_test.sh
./scripts/sync_to_nas_test.sh
```

`test.sh` auto-discovers all `cmd/*/go.mod` Go modules and all `scripts/*_test.sh` bash tests.

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
