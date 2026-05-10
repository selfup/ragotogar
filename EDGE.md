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

## Runtime (`cmd/edge`)

- Loads artifacts via `mmap` (`github.com/blevesearch/mmap-go`) for the
  vector blobs, postings, and payload bytes. Rowmaps and the payload
  offset table are read into memory at startup since they're small
  and the access pattern is dense.
- Accepts query string + per-lane toggles + merge strategy + cosine
  threshold + lexical/vector arm toggles + RRF fusion over HTTP at
  `GET /search?q=…`. Mirrors `cmd/web`'s URL params.
- Calls server-side encode endpoint for the query vector via
  `library.EmbedTexts` (same OpenAI-shaped HTTP contract `cmd/web` /
  `cmd/index` use, with the same retry layer).
- Per-enabled-lane flat int8 cosine scan with MAX-collapse via the
  rowmap sidecar.
- FST retrieval lane with **Snowball English (Porter2) stemming**
  matching pg's `to_tsvector('english')` so `airplane → airplan` /
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

Cold start budget at the live 3,564-photo corpus: ~150 ms to load
artifacts (mmap + parse manifest's id_space + parse rowmap+offset
tables), pg ping, FST `vellum.Open`. Reported in startup logs.

> **Parity callout — honored.** `library.StripNegation` and
> `library.ExtractNegation` are imported by `cmd/edge`, not
> reimplemented. Same code path → automatic parity with `cmd/web` for
> the negation parser. The runtime tokenizer's stemmer was added
> mid-flight after live data showed `pg`'s `to_tsvector('english')`
> stemming was a much wider parity gap than initially scoped — see
> `cmd/edge/fst_lane.go:tokenizeQuery`.

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

## Steps to ship

1. **`cmd/edge_build` v1** ✓ — produces all seven artifacts against
   `ragotogar_three_store_test`. Output sizes match estimates
   (~62 MB int8 vectors, 1.19 MB payload, 36 KiB FST).
2. **`cmd/edge` v1** ✓ — mmap loader, server-encode HTTP client via
   `library.EmbedTexts`, per-lane flat int8 cosine + MAX-collapse,
   FST retrieval lane with Snowball English stemming, RRF fusion,
   merge strategies on uint32, negation post-filter, phrase
   block at HTTP 400, JSON response with hits + per-arm timing.
   pg connect for liveness only at v1; hydration deferred to caller.
3. **Parity validation** — run a held-out query set through both
   `cmd/web` and `cmd/edge` against the same corpus. Confirm ranked
   ID overlap and divergence cases. (Separate session.)

This doc lives next to `ARCHITECTURE.md`. Update when a step ships or
a decision changes.
