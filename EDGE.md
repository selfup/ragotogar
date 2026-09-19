# Edge Search Artifact Pipeline

Status: **implemented / experimental**. `cmd/edge_build`, `cmd/edge`,
and the optional `cmd/web` backend selector run in tree today alongside
the existing Postgres retrieval path. See `ARCHITECTURE.md` for the main
pipeline; this document covers the parallel edge path.

## Why this exists

The default search path queries pgvector, optionally combines it with
Postgres full-text search, and can apply LLM filters. The edge path explores a different
shape: at corpus-seal time, export static read-only files and retrieve
from them through `cmd/edge`. The runtime loads files from disk with
`mmap`; embedding artifacts into an executable is not implemented.
Postgres remains the system of record and the source for web thumbnails,
full descriptions, and optional filtering/verification data.

The artifacts travel as files, but the current runtime is not standalone
offline search: startup requires a successful Postgres ping, and vector
queries call the configured embedding HTTP endpoint. Lexical-only requests
need no embedding call once the server is running. Both retrieval backends
coexist; Postgres remains the default in `cmd/web` and the only backend in
`cmd/search`.

After upgrading a library to v15 query isolation, rerun `cmd/edge_build` after
the description embeddings have been reindexed, then restart `cmd/edge`.
Existing sealed artifacts still contain the old description vectors and FTS
postings, including generated-query text. See README's v15 upgrade commands;
the database migration cannot repair artifacts already written to disk.

## Roles (kept separate)

1. **System of record (pg).** Owns ingestion output, embeddings, and
   thumbnails. The edge search handler does not query it.
2. **Build step (`cmd/edge_build`).** Reads pg and writes ten files
   (listed below). Unchanged source data yields stable artifact contents
   and `corpus_hash`, except for the manifest's `built_at` timestamp.
3. **Runtime (`cmd/edge`).** Loads artifacts, checks the configured model
   and dimension against the manifest, pings pg, and serves ranked names,
   compact IDs, captions, and tags. Its pg handle is used only for the
   startup liveness check; callers perform database hydration.
4. **Web integration (`cmd/web`).** Can send retrieval to edge while
   retaining query rewrite, classifier filtering, verification, sorting,
   thumbnail delivery, and full photo pages in the existing web pipeline.

```
query → cmd/edge → embedding HTTP endpoint (vector arm only)
            ↓
       3× flat int8 vector scans → merge union / intersect / weighted
            +
       FST lexical token coverage
            ↓
       RRF fusion when both arms are enabled
            ↓
       FST negation post-filter → optional topk → artifact payload
            ↓
       ranked compact IDs + names + captions + tags
            ↓
       caller hydrates from pg as needed
```

## Running the edge path

Use a populated, indexed library with a recorded embedding model identity.
Set `EMBED_MODEL` to the exact model used by `cmd/index`, and `EMBED_DIM`
if the model needs an explicit dimension override. For example, with the
library's default 4B model:

```bash
export EMBED_MODEL=text-embedding-qwen3-embedding-4b
./scripts/edge_build.sh -out /tmp/ragotogar-edge-v1 -embed-model "$EMBED_MODEL"
./scripts/edge.sh -artifacts /tmp/ragotogar-edge-v1
```

In another terminal, enable the web selector (using the same model and
library configuration):

```bash
./scripts/web.sh -edge-url http://127.0.0.1:8081
```

Select the edge backend in the UI (`?backend=edge`). Without `-edge-url`
the checkbox is hidden. `LIBRARY_DSN` or `-dsn` selects the library for
all three commands. `EMBED_ENDPOINT` (falling back to `LM_STUDIO_BASE`)
and `LLM_API_KEY` configure the runtime's query embedding requests.
All wrappers execute Go from source with `go run`.

Artifacts are a sealed export. After describe/classify/index changes,
finish ingestion and indexing, build into a **new output directory**, then
restart `cmd/edge` pointing at that directory. There is no hot reload or
atomic artifact publication: the builder creates/truncates individual
files and writes the manifest last. Do not overwrite files mapped by a
running server. A failed build can leave partial output.

The builder holds a shared embedding-configuration lock so model switches
wait for it, but its reads do not share a corpus-wide snapshot. Pause
source changes and incremental indexing during export for a consistent
corpus. Compact IDs are local to each export and may change on rebuild;
they are not stable photo IDs.

## Locked design decisions

| Decision | Resolution | Why |
|----------|------------|-----|
| FST library | `github.com/blevesearch/vellum` | Pure Go, no cgo, mmap-friendly, immutable after `Close()`, `uint64` values fit "offset into postings.bin" |
| ANN structure | Flat brute-force int8 cosine, per lane | 25,113 vectors × 2560 dimensions occupy about 61 MiB of vector bytes. Exhaustive scans avoid an ANN index; quantization can change thresholds and ranking. **Revisit at ~250K vectors.** |
| Vector quantization | int8 (L2-normalized → scaled by 127 → rounded) | One byte per component versus two for halfvec/fp16, excluding headers and rowmaps. Dot product divided by 127² approximates cosine; corpus-level recall parity remains unvalidated |
| Vector dimension | halfvec(dim) → int8(dim) | Read from the three database column types, including empty stores; written into `manifest.dim`. Supports 1024-dimensional Qwen3-Embedding-0.6B and 2560-dimensional 4B. The runtime checks its configured embedding dimension against the manifest. |
| Vector lanes | Three separate blobs (descriptions / metadata / queries) | Mirrors v12 toggle/merge semantics in `cmd/web`. Collapsing would be a search-surface redesign, out of brief scope. |
| Term universe (FST) | `descriptions.fts ‖ exif.fts` only | Mirrors current `cmd/web` FTS surface exactly. Classifier enums and query phrasings are real new ground; deferred. |
| Payload location | Separate `payload.bin` blob | Avoids polluting manifest startup parse with ~700 KB of base64; gives a versioning seam for payload schema. |
| Payload contents | `caption` = `descriptions.subject`; `tags` = `subject_altitude`, `scene_indoor_outdoor`, `scene_time_of_day`, `scene_weather`, `pov_container` | Tile-display sized. Full prose stays in pg (hydrate on demand). |
| Payload presence guarantee | One record per compact-id, always | LEFT JOIN against `descriptions` and `classified` so unclassified / undescribed photos still get a record. `caption` and individual `tags[i]` may be empty strings. **Edge runtime can rely on `payload[compact_id]` always being readable** — no gap between FST lookup (which returns compact ids regardless) and payload lookup. |
| Query-side embedding | `cmd/edge` calls the configured embedding endpoint | No model inference implementation is bundled with the runtime. |
| `embedder_version` | **Per lane**, in manifest | All three lanes currently use one exact model ID. Runtime checks each against its configured model at startup; dimensions are checked separately. Weights changing behind an unchanged model ID are not detectable. |
| `embedder_version` source at build time | `--embed-model` flag, checked against `embedding_config` | The database records the model used to index all three stores. Builds reject unknown identities and mismatched labels before writing artifacts, without probing the live endpoint. Changing models requires a full reindex before rebuilding. |
| Toggle state location | Runtime API, not artifact | Artifact ships all three lanes; edge honors per-query toggles like `cmd/web`. |
| Hydration | Caller loads full records and thumbnails from pg | Edge itself only pings pg at startup; it has no hydration endpoint. |

## Artifacts

```
terms.fst                          vellum: lexeme → uint64 offset into postings.bin
postings.bin                       per-term varint-packed compact-id deltas
vectors.descriptions.bin           flat int8 [N_d × dim]
vectors.descriptions.rowmap.bin    uint32 LE × N_d, row → compact-id
vectors.metadata.bin               flat int8 [N_m × dim]
vectors.metadata.rowmap.bin        uint32 LE × N_m, row → compact-id
vectors.queries.bin                flat int8 [N_q × dim]
vectors.queries.rowmap.bin         uint32 LE × N_q, row → compact-id
payload.bin                        fixed-offset records, one per compact-id
manifest.json                      schema_version, corpus_hash, built_at, dim,
                                   quantization, lanes{embedder_version, rows},
                                   id_space{count, names[]}, payload{tags[]}
```

`id_space.names[i]` maps internal compact `uint32` (0-indexed) →
`photos.name`. Every blob is keyed by this compact id.

All three vector lanes ship a rowmap sidecar — uniform handling at the
runtime, no implicit "row index = compact id" assumption to maintain.
The queries lane has ~5 phrasings per photo so the rowmap is load-
bearing; descriptions and metadata are 1 row per photo today but the
schema permits multiple chunks per photo, and the sidecar costs ~14 KB
per dense lane.

## Manifest schema (v1)

```json
{
  "schema_version": 1,
  "corpus_hash": "<sha256>",
  "built_at": "<RFC3339>",
  "dim": 2560,
  "quantization": "int8",
  "lanes": {
    "descriptions": { "embedder_version": "<exact model ID>", "rows": 3564 },
    "metadata":     { "embedder_version": "<exact model ID>", "rows": 3564 },
    "queries":      { "embedder_version": "<exact model ID>", "rows": 17985 }
  },
  "id_space": { "count": 3564, "names": ["..."] },
  "payload": { "tags": ["subject_altitude", "scene_indoor_outdoor",
                        "scene_time_of_day", "scene_weather", "pov_container"] }
}
```

`corpus_hash` v1: `sha256(sorted(photos.name) ‖
max(inference.described_at) ‖ max(classified.classified_at))`. Cheap;
detects re-describe / re-classify across the whole corpus.

**Known collision case**: re-describing a single photo whose
`described_at` doesn't exceed the existing max leaves the hash
unchanged. Vector-only reindexing and metadata edits are also absent from
the hash inputs. It is not an artifact checksum. No downstream consumer currently
depends on `corpus_hash` for cache invalidation or artifact equality.
TODO: refine to a per-photo timestamp aggregate when such a consumer
appears.

## Build step (`cmd/edge_build`)

Single Go binary. Reads from `LIBRARY_DSN` (default
`postgres:///ragotogar` — operator overrides for the three-store DB).
Outputs the ten artifact files to a directory; no inference calls are needed.

Flags (v1):

- `-dsn` — pg DSN (overrides `LIBRARY_DSN`)
- `-out` — required output directory (created if missing)
- `-embed-model` — required; must match the model recorded in the database; recorded per lane

Pipeline (after model identity/dimension validation and acquiring the shared
configuration lock):

1. **Read photos.** `SELECT name FROM photos ORDER BY name COLLATE "C"` —
   establishes the compact-id space. `id_space.names[i] = photos.name`.
2. **Read FTS lexemes.** `SELECT lexeme, photo_id FROM (SELECT photo_id,
   unnest(tsvector_to_array(fts)) FROM descriptions UNION ALL ... FROM
   exif) ORDER BY lexeme COLLATE "C", photo_id COLLATE "C"`. SQL does the
   byte-order sort required by vellum and delta-encoded compact IDs.
3. **Build FST + postings via `fstWriter`.** Pure-function core that
   takes sorted `(lexeme, compact_id)` pairs, groups by lexeme, writes
   varint-packed delta-encoded compact-id lists to `postings.bin`,
   records the byte offset, and calls `vellum.Insert(lexeme, offset)`
   when the group flushes. The writer validates ordering eagerly at
   every `Add` — out-of-order lexeme or compact_id within a group
   surfaces immediately rather than at Close, which would let the
   build stream thousands of postings to disk before erroring (and
   uint32 underflow on a backwards delta would silently corrupt the
   posting list rather than fail).
4. **Build vector lanes.** For each lane:
   a. `SELECT photo_id, embedding FROM photo_<lane> ORDER BY ...`
   b. halfvec → fp32 → L2-normalize → int8 (`round(x * 127)`,
      asymmetric saturating clamp at ±127 — never -128)
   c. Write flat int8 array to `vectors.<lane>.bin`
   d. Write `vectors.<lane>.rowmap.bin` (uint32 LE per row → compact id)
5. **Build payload.** Per compact-id, encode `caption`
   (`descriptions.subject`) + 5 classifier enums (in
   `payloadTagFields` order) as varint-len-prefixed strings. Write
   header (count + offset table) followed by records.
6. **Write manifest.** Query `MAX(described_at)` from `inference` and
   `MAX(classified_at)` from `classified`; compute `corpusHash` (pure
   function); populate per-lane `embedder_version` from
   `--embed-model`; serialize JSON.

### Tests

The suite includes pure-function tests, property tests, and temporary
Postgres integration databases through `library/testdb`:

| Area | Coverage |
|------|----------|
| Artifact encoding | Quantization fidelity and invariants, FST ordering/deduplication, posting and payload encoding, compact IDs, and corpus hashing |
| Database export | Bytewise collation, live FST/postings export, rejection of unknown or mismatched embedding identities |
| Full builder round-trip | Seed pg, run the builder, reopen/decode artifacts, validate rowmaps/payload/manifest at both 1024 and 2560 dimensions, check hash repeatability |
| Runtime | Posting/payload decoding, tokenization and stemming, vector scans and MAX-collapse, merge strategies, RRF, negation, model/dimension checks, and goroutine-leak detection |
| Web client | HTTP response/error handling, cancellation, query parameters, and lexical/vector routing |

Run focused checks with:

```bash
go test -race ./cmd/edge/... ./cmd/edge_build/... ./cmd/web/...
```

Use `./test.sh` for the full suite. Database tests skip when Postgres is
unavailable. `./bench.sh` includes edge scan,
quantization, decoding, tokenization, merge/fusion-related hot paths.

The builder round-trip decodes files independently; it does not pass them
through the runtime's `openArtifacts` loader and HTTP handler. A complete
builder-to-running-server test and held-out pg/edge retrieval comparison
remain gaps.

## Runtime (`cmd/edge`)

- Loads artifacts via `mmap` (`github.com/blevesearch/mmap-go`) for the
  vector blobs, postings, and payload bytes. Rowmaps and the payload
  offset table are read into memory at startup since they're small
  and the access pattern is dense.
- Accepts query string + per-lane toggles + merge strategy + cosine
  threshold + lexical/vector arm toggles over HTTP at `GET /search?q=…`.
  Both arms enabled selects RRF fusion; there is no separate fusion toggle.
- Calls server-side encode endpoint for the query vector via
  `library.EmbedTexts` (same OpenAI-shaped HTTP contract `cmd/web` /
  `cmd/index` use, with the same retry layer).
- Per-enabled-lane flat int8 cosine scan with MAX-collapse via the
  rowmap sidecar.
- FST retrieval lane with **Snowball English (Porter2) stemming**
  using the same stemmer family as pg's `to_tsvector('english')` so `airplane → airplan` /
  `propeller → propel` / `engine → engin` line up with the stems pg
  wrote into the FST at build time. Without stemming the FST arm
  contributes ~0 for descriptive queries; with it, the arm
  contributes hundreds of hits per token.
- Coverage rank as the FST scoring function (count of query tokens
  matched per doc). Approximates pg's `ts_rank` for short queries
  without per-term IDF storage in the artifact.
- RRF fusion of vector arm + FST arm (k=60, matching `library.RRFK`).
- Negation post-filter: `library.ExtractNegation` produces the
  negation tokens, FST lookup of each (also stemmed) yields a drop
  set, applied after merge.
- Phrase queries blocked at HTTP 400 — the FST has no position info,
  so adjacency can't be reproduced without a silent over-match.
- Returns JSON `{compact_id, name, caption, tags, score}` per hit
  plus per-arm timing for diagnostics.
- pg hydration is left to the caller; cmd/edge's pg handle is open
  for liveness only at v1.

Historical cold-start observation on a 3,564-photo corpus: ~150 ms to load
artifacts (mmap + parse manifest's id_space + parse rowmap+offset
tables), pg ping, FST `vellum.Open`. This is not a startup SLA;
current logs report loaded sizes and row counts, not total startup time.

### HTTP API and search differences

The server defaults to `127.0.0.1:8081`; `-addr` overrides it. `GET /health`
returns `ok`, `corpus_hash`, `photos`, `schema`, and `quantization` from the
loaded manifest. It does not recheck Postgres or the embedding endpoint.

| `/search` parameter | Default | Meaning |
|---------------------|---------|---------|
| `q` | required | Query text; any double quote returns HTTP 400, even with `lexical=0` |
| `vector`, `lexical` | `1`, `1` | Enable vector and FST arms; at least one must be enabled |
| `descriptions`, `metadata`, `queries` | all `1` | Vector lanes; at least one required when `vector=1` |
| `merge` | `union` | `union`, `intersect`, or `weighted`; unknown values fall back to union |
| `wd`, `wm`, `wq` | all `1` | Weights used by `weighted` merge |
| `cosine` | `0.50` | Approximate cosine floor per vector lane |
| `topk` | `0` | Positive result cap applied after fusion and negation; zero is unbounded |

Responses contain `query`, `stripped_query`, `negation`, `elapsed_ms`,
`vector_arm`/`fst_arm` result counts and timings, `fused_total`,
`after_negation`, and `hits` with `compact_id`, `name`, `caption`, `tags`,
and `score`. Scores represent vector merge output, token coverage, or RRF
according to the enabled arms; they are not interchangeable cosine values.
Embedding failures return HTTP 502 (the call has a 30-second context),
and artifact scan failures return HTTP 500. A payload decode error keeps
the hit with empty caption/tags.

**Lexical parity is partial.** The FST stores lexeme membership without
positions or frequencies. Retrieval unions matches for any positive token
and ranks by matched-token count. Bare words do not enforce Postgres AND,
and `OR` is not parsed as a boolean operator. There is no `ts_rank` or
relative FTS cutoff. Tokenization and stemming approximate English pg FTS,
with punctuation and Unicode differences documented below. Vector scans
use quantized exhaustive search instead of pgvector HNSW, so rankings and
threshold decisions can differ. Equal vector/merge scores have no explicit
tie-break; FST coverage and final RRF ties use ascending compact ID.

Negation parsing reuses `library.StripNegation` and
`library.ExtractNegation`. Edge then drops the union of matching negated
stem postings after fusion. Parser reuse does not establish full Postgres
query-semantic parity.

**Web routing:** vector modes disable the FST arm; FTS+vector modes enable
both. Auto modes rewrite in `cmd/web` and then use FTS+vector routing.
Classifier filtering and verification also stay in `cmd/web`, using live
Postgres records and their existing caches. The web client forwards lane,
merge, weight, and cosine settings, but not the `fts ≥` threshold; that
slider has no effect at edge. It consumes names and scores, leaving edge
payloads and per-arm timing out of the existing grid renderer.

Quoted input or a rewrite that introduces quotes produces an error in the
web UI. Edge retrieval errors do not trigger a fallback to pg. Choose the
Postgres backend when phrase or full boolean semantics are needed.

## Out of scope (per brief)

- WASM
- pg schema redesign
- Replacing pg with anything else
- Streaming / incremental index updates (rebuild-on-corpus-seal is the
  model)
- Hybrid score fusion strategies beyond a documented baseline
- Generation inside `cmd/edge` (embedding HTTP calls are implemented;
  optional rewrite/filter/verify remain in `cmd/web`)
- Performance micro-optimization before the pipeline runs end-to-end
- Multi-corpus / multi-shoot artifact management
- Update / delete semantics within a sealed corpus
- Network protocol design between edge and pg (assume direct pg over
  user's existing setup)
- Auth, secrets, deployment

## Pg → edge parity audit (the bug class)

Two bugs already shipped were the same shape: a pg-side default
behavior diverging from what the edge expected. This section
itemizes the audit so the *next* class member doesn't ambush a
release the same way.

The shape of the class:

> A function that takes pg output and feeds it to a Go consumer with
> stricter input requirements than pg's defaults satisfy.

| Gap | What pg does by default | What edge expects | Status |
|-----|-------------------------|-------------------|--------|
| **Lexicographic ordering** | `lc_collate` (e.g. en_US.UTF-8) reorders punctuation + interleaves case | byte-wise lex (vellum requires it) | Fixed: `ORDER BY <text-col> COLLATE "C"` on every text sort in the build path. Integration test in `cmd/edge_build/integration_test.go` catches regressions. |
| **English morphology** | `to_tsvector('english')` stems via Porter2 (`airplane → airplan`) | tokenizeQuery uses raw query tokens for FST.Get | Fixed: `cmd/edge/fst_lane.go:tokenizeQuery` applies the same Porter2 stemmer (`github.com/blevesearch/snowballstem`). Known-answer test pins parity. |
| **English stopwords** | `to_tsvector('english')` drops `the`, `and`, `of`, … from indexed lexemes | tokenizeQuery passes them through | Acceptable: stopwords miss FST (not stored at index time), contribute nothing, no recall hit. Documented in `tokenizeQuery` comment. |
| **Schema version filtering** | `cmd/index` writes rows at `schema_version=2`; future bumps may UPSERT-delete old rows | `cmd/edge_build` doesn't filter by schema_version on the v12 stores | Latent: works today because schema_version=2 is the only value present. Future-proofing would add `WHERE schema_version = 2` to the vector lane queries; not a bug now. |
| **Unicode normalization** | `to_tsvector` is locale-aware but doesn't NFC/NFD-normalize | edge tokenizer preserves Go-string bytes | Latent: corpus is ASCII-heavy English, mismatch unlikely to bite. Worth re-auditing if a non-ASCII-heavy corpus is added. |
| **Case folding** | `to_tsvector('english')` lowercases | tokenizeQuery lowercases via `strings.ToLower` | Match. |
| **Token boundaries** | `to_tsvector` splits on word boundaries (whitespace, most punctuation) | tokenizeQuery splits on `unicode.IsLetter` and `unicode.IsDigit` | Approximate match; minor edge cases (e.g. internal apostrophes) may diverge. Latent. |
| **Numeric tokens** | `to_tsvector` keeps numbers as separate lexemes | tokenizeQuery keeps numbers | Match. |

The lesson — stated for the next time this class shows up:

> Any function that consumes pg output and has a strict input
> requirement (sortedness, normalized form, specific token shape) is
> a candidate. **Test it against pg directly**, not just synthetic
> Go-constructed inputs. Pure-function unit tests can't cover this
> class because the bug lives in the pg-Go interface, not in either
> language alone.

## Future work (not blocking)

- **Manifest scaling.** `id_space.names[]` is JSON-inline today (~124 KB
  at 3.5K photos; ~1 MB at 30K). When that feels heavy, split into a
  compact `id_space.bin` (varint-len-prefixed names back-to-back) and
  leave only `count` in the manifest. Migration is a manifest
  `schema_version` bump.
- **Per-lane `--embed-model` overrides.** v1 takes one `--embed-model`
  flag and applies it to all three lanes. If a future build needs
  divergent embedders per lane, add `--embed-model-{descriptions,metadata,queries}`
  flags; the per-lane manifest field already exists.
- **`corpus_hash` per-photo aggregate.** Refine when a downstream
  consumer (cache invalidation, artifact equality check) needs to
  detect single-photo re-describes. See manifest section above.

## Implementation status

1. **`cmd/edge_build` v1** ✓ — produces all ten artifact files, with database model identity
   validation and dimensions derived from the vector columns.
2. **`cmd/edge` v1** ✓ — mmap loader, server-encode HTTP client via
   `library.EmbedTexts`, per-lane flat int8 cosine + MAX-collapse,
   FST retrieval lane with Snowball English stemming, RRF fusion,
   merge strategies on uint32, negation post-filter, phrase
   block at HTTP 400, JSON response with hits + per-arm timing.
   pg connect for liveness only at v1; hydration deferred to caller.
3. **Web backend integration** ✓ — `-edge-url` enables a per-query
   selector; retrieval swaps to edge and other web stages remain in place.
4. **Parity validation** — run a held-out query set through both
   `cmd/web` and `cmd/edge` against the same corpus. Confirm ranked
   ID overlap and divergence cases. (Separate session.)

This doc lives next to `ARCHITECTURE.md`. Update when a step ships or
a decision changes.
