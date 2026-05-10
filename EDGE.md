# Edge Search Artifact Pipeline

Status: **novel / experimental**. Built alongside the existing pg-runtime
search (`cmd/web` / `cmd/search`), not a replacement. The current
architecture stays as documented in `ARCHITECTURE.md`; this doc scopes
the parallel edge path.

## Why this exists

The current search path keeps Postgres in the hot loop — every query
hits pgvector + websearch_to_tsquery + (optional) LLM. That's correct
for the dev workflow it serves. The edge path explores a different
shape: at corpus-seal time, build static read-only artifacts that a Go
binary can `mmap` or `go:embed`, and serve search out of those
artifacts with no pg in the query path. pg stays as the system of
record and the hydration store (thumbnails, full prose, anything the
artifact intentionally doesn't carry).

The result is a binary that can search a sealed corpus offline, with
artifacts that travel as files. **No replacement of pg-runtime search.**
The two paths coexist.

## Roles (kept separate)

1. **System of record (pg, unchanged).** Ingestion, vision pipeline output,
   embeddings, history, thumbnails/blobs. Always live. Not in the edge
   query path.
2. **Build step (`cmd/edge_build`, new, offline).** Reads pg. Produces
   seven static artifacts (below). Idempotent — same pg state in →
   same artifacts out.
3. **Runtime (`cmd/edge`, new, edge).** Loads the artifacts. Serves
   search. Returns ranked photo IDs + small payload. **pg is not in
   the search path.** pg *is* in the path for hydration — once search
   returns IDs, the runtime fetches thumbnails/full prose from pg.

```
query → server-side encode → vector
            ↓
       cmd/edge: FST lexical lane + 3× flat int8 ANN lanes
            ↓
       merge per strategy (union / intersect / weighted)
            ↓
       ranked compact-IDs + payload (subject + tags)
            ↓
       caller hydrates from pg as needed (thumbnail, full description)
```

## Locked design decisions

| Decision | Resolution | Why |
|----------|------------|-----|
| FST library | `github.com/blevesearch/vellum` | Pure Go, no cgo, mmap-friendly, immutable after `Close()`, `uint64` values fit "offset into postings.bin" |
| ANN structure | Flat brute-force int8 cosine, per lane | 25,113 vectors × 2560-dim int8 = ~61 MB total. ~12-30 ms scan on M-series. No build complexity, deterministic recall. **Revisit at ~250K vectors.** |
| Vector quantization | int8 (L2-normalized → scaled by 127 → rounded) | ~5× smaller than fp16, ~1-3% recall cost, fits `go:embed`, dot product approximates cosine after scaling |
| Vector dimension | halfvec(2560) → int8(2560) | Native Qwen3-Embedding-4B output. No Matryoshka truncation. |
| Vector lanes | Three separate blobs (descriptions / metadata / queries) | Mirrors v12 toggle/merge semantics in `cmd/web`. Collapsing would be a search-surface redesign, out of brief scope. |
| Term universe (FST) | `descriptions.fts ‖ exif.fts` only | Mirrors current `cmd/web` FTS surface exactly. Classifier enums and query phrasings are real new ground; deferred. |
| Payload location | Separate `payload.bin` blob | Avoids polluting manifest startup parse with ~700 KB of base64; gives a versioning seam for payload schema. |
| Payload contents | `caption` = `descriptions.subject`; `tags` = `subject_altitude`, `scene_indoor_outdoor`, `time_of_day`, `weather`, `pov_container` | Tile-display sized. Full prose stays in pg (hydrate on demand). |
| Payload presence guarantee | One record per compact-id, always | LEFT JOIN against `descriptions` and `classified` so unclassified / undescribed photos still get a record. `caption` and individual `tags[i]` may be empty strings. **Edge runtime can rely on `payload[compact_id]` always being readable** — no gap between FST lookup (which returns compact ids regardless) and payload lookup. |
| Query-side embedding | Server encodes; edge does ANN only | Edge stays small; server is responsible for embedder version. |
| `embedder_version` | **Per lane**, in manifest | Each store could in principle use a different embedder; per-lane field surfaces silent drift on first query when server-encode reports a mismatch. |
| `embedder_version` source at build time | `--embed-model` flag (operator-asserted) | Probing the live endpoint at build time would assert current state equals build-time state — exactly the silent-drift mode the field exists to catch. Operator trust is the right tool. |
| Toggle state location | Runtime API, not artifact | Artifact ships all three lanes; edge honors per-query toggles like `cmd/web`. |
| Hydration | pg connection required at runtime; loud failure on unreachable | Brief: don't silently degrade to no-thumbnails. |

## Artifacts

```
terms.fst                          vellum: lexeme → uint64 offset into postings.bin
postings.bin                       per-term varint-packed compact-id deltas
vectors.descriptions.bin           flat int8 [N_d × 2560]
vectors.descriptions.rowmap.bin    uint32 LE × N_d, row → compact-id
vectors.metadata.bin               flat int8 [N_m × 2560]
vectors.metadata.rowmap.bin        uint32 LE × N_m, row → compact-id
vectors.queries.bin                flat int8 [N_q × 2560]
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
    "descriptions": { "embedder_version": "<model>@<dim>", "rows": 3564 },
    "metadata":     { "embedder_version": "<model>@<dim>", "rows": 3564 },
    "queries":      { "embedder_version": "<model>@<dim>", "rows": 17985 }
  },
  "id_space": { "count": 3564, "names": ["..."] }
}
```

`corpus_hash` v1 sketch: `sha256(sorted(photos.name) ‖
max(inference.described_at) ‖ max(classified.classified_at))`. Cheap;
detects re-describe / re-classify across the whole corpus.

**Known collision case**: re-describing a single photo whose
`described_at` doesn't exceed the existing max leaves the hash
unchanged. Acceptable for v1 because no downstream consumer currently
depends on `corpus_hash` for cache invalidation or artifact equality.
TODO: refine to a per-photo timestamp aggregate when such a consumer
appears.

## Build step (`cmd/edge_build`)

Single Go binary. Reads from `LIBRARY_DSN` (default
`postgres:///ragotogar` — operator overrides for the three-store DB).
Outputs the seven artifacts to a directory.

Flags (v1):
- `-dsn` — pg DSN (overrides `LIBRARY_DSN`)
- `-out` — output directory (created if missing)
- `-embed-model` — operator-asserted embedder version recorded per lane

Pipeline:
1. **Read photos.** `SELECT name FROM photos ORDER BY name` —
   establishes the compact-id space. `id_space.names[i] = photos.name`.
2. **Read FTS lexemes.** `SELECT lexeme, photo_id FROM (SELECT photo_id,
   unnest(tsvector_to_array(fts)) FROM descriptions UNION ALL ... FROM
   exif) ORDER BY lexeme, photo_id`. SQL does the sort.
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

`cmd/edge_build` ships with 34 unit-level test cases across 6 files —
all pure-function, no live DB. The DB-touching wrappers (`loadIDSpace`,
`writeLane`, `buildPayload`, `buildFSTAndPostings`) are tested
indirectly via the live build against `ragotogar_three_store_test`,
which is fast (~4 s) and reproducible (`corpus_hash` is identical
across runs against unchanged pg state).

| File | Coverage |
|------|----------|
| `vectors_test.go` | `quantizeInt8` — zero, determinism, ±127 floor, sign preservation, in-range under adversarial input, **cosine fidelity vs fp32** (max error ~0.009 across 100 trials at 2560-dim, threshold 0.012) |
| `fst_test.go` | `fstWriter` round-trip via vellum reopen + posting decode, dup-guard, eager order validation (lexeme + within-group compact_id), empty build, multi-byte-varint compact-ids |
| `payload_test.go` | `encodePayloadRecord` round-trip (empty/unicode/long-caption), empty-record byte-shape lock, `payloadTagFields` contract lock |
| `manifest_test.go` | `corpusHash` determinism, name + name-order sensitivity, both timestamp sensitivities, null-vs-valid, swapped-timestamp distinction, separator-byte guard |
| `idspace_test.go` | `idSpace.CompactID` round-trip + missing name + empty |
| `main_test.go` | `humanBytes` boundaries |

## Runtime (`cmd/edge`) — sketch only, not in this commit

- Loads artifacts via mmap (default) or `go:embed` (build-tag opt-in).
  Pick mmap for v1 — flexible, no binary bloat, easy to swap artifacts
  without rebuilding.
- Accepts query string + per-lane toggles + merge strategy + cosine
  threshold over HTTP (mirrors `cmd/web` knobs).
- Calls server-side encode endpoint for the query vector.
- FST lexical lookup with negation parse mirroring
  `library.ExtractNegation`.
- Per-enabled-lane flat int8 cosine scan.
- Merge per strategy.
- Return ranked compact-ids + payload.
- Caller hydrates from pg.

Cold start budget: artifact `mmap` + pg connection. Both reported.

## Out of scope (per brief)

- WASM
- pg schema redesign
- Replacing pg with anything else
- Streaming / incremental index updates (rebuild-on-corpus-seal is the
  model)
- Hybrid score fusion strategies beyond a documented baseline
- Generation / LLM integration
- Performance micro-optimization before the pipeline runs end-to-end
- Multi-corpus / multi-shoot artifact management
- Update / delete semantics within a sealed corpus
- Network protocol design between edge and pg (assume direct pg over
  user's existing setup)
- Auth, secrets, deployment

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

## Steps to ship

1. **`cmd/edge_build` v1** — produces all seven artifacts against
   `ragotogar_three_store_test`. Validate output sizes against
   estimates (~61 MB int8 vectors total, ~700 KB payload, FST size
   measured-not-predicted).
2. **`cmd/edge` v1** — mmap artifacts, server-encode HTTP client,
   per-lane flat int8 cosine, FST lexical lane, merge, payload return,
   pg hydration call. (Separate session.)
3. **Parity validation** — run a held-out query set through both
   `cmd/web` and `cmd/edge` against the same corpus. Confirm ranked
   ID overlap and divergence cases. (Separate session.)

This doc lives next to `ARCHITECTURE.md`. Update when a step ships or
a decision changes.
