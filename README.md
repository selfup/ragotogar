# Ragotogar

**RAG Photo Organizer** — describe, organize, and search your photo library with local or OpenAI-compatible models. *(Yes, it's a palindrome.)*

Photo originals stay on disk. Postgres stores descriptions, EXIF metadata, thumbnails, classifications, and vector indexes. Search combines scene descriptions, capture metadata, and generated query phrases, with optional keyword search and LLM verification.

**Research preview:** under active development.

## Get started

The setup below uses macOS, Homebrew, **Go 1.26.1+**, and [LM Studio](https://lmstudio.ai/). The media organizer requires macOS. Run commands from the repo root.

Install the image tools, then initialize Postgres 18, pgvector, and the library schema:

```bash
brew install exiftool imagemagick
./scripts/bootstrap.sh
```

Start LM Studio's API server at `http://localhost:1234` and load these models:

| Role | Model |
|------|-------|
| Describe photos (vision) | `qwen/qwen3-vl-8b` |
| Classify and verify (text) | `mistralai/ministral-3-3b` |
| Index and search (embeddings) | `text-embedding-qwen3-embedding-4b` |

Describe, classify, and index a photo directory, then start the browser UI:

```bash
LM_MODEL=qwen/qwen3-vl-8b ./scripts/full_run.sh /path/to/photos
./scripts/web.sh
```

Open <http://localhost:8080>. Subsequent runs skip existing work. The **describe** page can also ingest a directory or single photo; choose **index after classify** to make successfully classified photos searchable.

To use another database, set `LIBRARY_DSN` (default: `postgres:///ragotogar`). Separate model servers use `VISION_ENDPOINT`, `TEXT_ENDPOINT`, and `EMBED_ENDPOINT`; authenticated providers use `LLM_API_KEY`. See the [configuration reference](docs/REFERENCE.md#model-endpoints-and-routing) for provider routing and per-command settings.

## Search

Start with **vector** mode and a query like `bedroom with warm light`. Add **+verify** for a relevance check on each candidate, or choose **auto** to rewrite natural language for keyword and vector retrieval. Both use the text model.

**FTS+vector** adds keyword matches. It supports quoted phrases, uppercase `OR`, and exclusions such as `-monochrome`. Exclusions apply to both retrieval arms; positive phrases constrain the keyword arm. See [query syntax](docs/REFERENCE.md#query-syntax) and the [search playbook](skills/search_skill.md) for examples and tuning.

From the terminal:

```bash
./scripts/search.sh "bedroom with warm light"
./scripts/search.sh -retrieve -verify "planes on taxiways"
```

## Update the library

For more photos, rerun the ingestion command with their directory. To refresh existing descriptions and their search indexes:

```bash
./scripts/photo_describe.sh -force -classify /path/to/photos
./scripts/index.sh -reindex=descriptions,metadata,queries
```

Re-describing alone leaves existing embeddings in place. Keep the same embedding model and dimensions for indexing and search; [switching models](docs/REFERENCE.md#indexing-and-search) requires a full reindex.

Optional file organization moves media into type/date folders and reunites sidecars:

```bash
./scripts/organize.sh /path/to/media
```

Use `-mtime` when copied files share the same creation date. NAS sync is separate; it requires rclone or rsync.

## More documentation

| Need | Guide |
|------|-------|
| CLI flags, configuration, schema, upgrades, analysis, and utilities | [Command reference](docs/REFERENCE.md) |
| Search examples and troubleshooting | [Search playbook](skills/search_skill.md) |
| Code structure, design decisions, and roadmap | [Architecture](ARCHITECTURE.md) |
| Experimental retrieval from static artifacts | [Edge search](EDGE.md) |
| Multiple llama.cpp backends | [Replica proxy](docs/REFERENCE.md#replica-proxy) |

Stable photo IDs and exact numerical search filters remain roadmap work. The edge backend needs artifact rebuilds after library changes and has different keyword semantics.

## Development

```bash
./test.sh      # Go and shell tests; race detector enabled
./bench.sh     # Go benchmarks
```

The root Go module covers most commands and `library/`; `cmd/describe` and `cmd/organize` have separate modules. `test.sh` runs all three. Database tests use temporary databases and skip when Postgres or pgvector is unavailable. Programs run from source with `go run`.

[MIT license](LICENSE.md).
