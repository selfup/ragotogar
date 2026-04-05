# Strategies

Operational choices that aren't obvious from the code alone — the *why* behind particular model selections, pipeline shapes, and trade-offs we've validated through testing. Update when a strategy changes or a new one is adopted.

## Three-slot model architecture

The RAG pipeline has three distinct LLM workloads. They *can* be sized independently, but we default all three to Ministral 3B for operational simplicity — one model handles vision description, index entity extraction, and query synthesis. Override `SEARCH_MODEL` per-query when a specific query needs stronger multi-document synthesis.

| Slot | Env var | Default | Why |
|---|---|---|---|
| **Vision description** (`cmd/describe`) | `LM_MODEL` | `mistralai/ministral-3-3b` | 2.5–2.9× faster than devstral; competitive OCR (7/8 on large clear text vs devstral's 3/8); matches devstral on entity density |
| **Index entity extraction** (LightRAG ingest) | `INDEX_MODEL` | `mistralai/ministral-3-3b` | Validated on 26-photo test set: 231 nodes / 231 edges, ~9 entities/photo, 2:52 wall-clock, semantic concept normalization works (e.g. "elevated platform with guardrail" → `Bridge` entity) |
| **Query synthesis** (LightRAG query) | `SEARCH_MODEL` | `mistralai/ministral-3-3b` | Adequate for `naive`/`local` and for most single-photo synthesis queries. For multi-document synthesis (`global`/`hybrid` over many chunks), override to a bigger model per query — see below. |

The first two are batch workloads where throughput matters and continuous batching across LightRAG's 8 async workers is the main scaling lever. The third is interactive — one query at a time, latency is tolerable.

Keeping everything on a single 3B model means:
- Only one LLM loaded in LM Studio for the full pipeline (plus the embedding model)
- No JIT load traps when switching between describe, index, and search
- Consistent GPU/VRAM footprint regardless of which workload is running
- Simpler operational story, fewer config surfaces

The trade-off — and the reason to know the SEARCH override exists — is multi-document synthesis quality.

### Known limitation: multi-document synthesis on small models

Same graph (231 nodes / 231 edges from Ministral-indexed photos), same retrieval (20 relevant chunks passed to the synthesis LLM), same query (`"roadtrip in winter"` in `global` mode):

- **Small model (4B)**: answer cites **1 photo** (the one with the strongest single-keyword match). Ignores 19 other relevant chunks.
- **devstral-small-2-2512 (24B GGUF)**: answer cites **9 photos** spanning the full March 21 road trip, organized into Road Conditions / Vehicle Prep / Route Planning / Safety Tips sections.

The retrieval pipeline surfaced identical context in both cases — only the model writing the final paragraph changed. Small models in the 3–4B range are genuinely limited at synthesizing across many chunks; they fixate on the chunk with the strongest surface-level match and ignore the rest. This is not an indexing problem, not a prompt problem, and not a retrieval problem — it's a raw model capacity ceiling.

### When to override `SEARCH_MODEL`

| Mode | Ministral 3B (default) | Override to 24B? |
|---|---|---|
| `naive` — direct vector retrieval, one dominant answer | ✓ Fine | No |
| `local` — entity neighborhood, tight answer | ✓ Usually fine | Only if answer feels incomplete |
| `global` — community summaries, broad thematic answer | ✗ Cites too few photos | **Yes, override** |
| `hybrid` — local + global merged | ✗ Cites too few photos | **Yes, override** |

Per-query override:

```bash
SEARCH_MODEL="mistralai/devstral-small-2-2512" ./tools/search.sh --mode global "roadtrip in winter"
```

You need devstral loaded in LM Studio for this to work. If you run the override command without the model loaded and LM Studio has JIT auto-load enabled, it'll load devstral on top of whatever else is running — which on an M3 Ultra is fine VRAM-wise but worth knowing about. Preloading avoids the surprise:

```bash
lms load mistralai/devstral-small-2-2512 --context-length 32000 --parallel 4
```

## Hybrid photo description: Ministral default + devstral augmentation

Ministral-alone is a functional pipeline — it was validated end-to-end on a 26-photo test set with working description, indexing, and retrieval. Devstral augmentation is **optional enrichment** that catches visual details Ministral misses. It is not a fallback or correctness dependency.

### Pipeline

1. **Fast path — Ministral 3B (always):**
   ```bash
   ./scripts/batch_photo_describe.sh -output descriptions/ministral /Volumes/T9/X100VI/JPEG/March
   ```
   ~4.6s per photo. This alone is sufficient to build a working LightRAG graph.

2. **Enrichment path — devstral-small-2-2512 GGUF (periodic, optional):**
   ```bash
   ./scripts/batch_photo_describe.sh \
     -output descriptions/devstral \
     -model mistralai/devstral-small-2-2512 \
     /Volumes/T9/X100VI/JPEG/March
   ```
   ~13s per photo. Run weekly or after big imports with high detail density. Background job, re-run safe, interruptible.

3. **Index both (LightRAG reads recursively):**
   ```bash
   ./tools/index_and_vectorize.sh descriptions/
   ```
   LightRAG finds JSONs at any depth via `descriptions/**/*.json`, extracts entities from each, and merges by entity name. Overlapping entities (`Walmart`, `Yale Ave`) collapse to single graph nodes; unique entities from each model (`drive4walmart.com` from Ministral, `soccer goalposts` from devstral) become new nodes on the same photo. No custom merge code needed.

To roll back devstral augmentation: delete `descriptions/devstral/` and re-index.

### What each model catches that the other misses

| Caught by Ministral | Caught by devstral |
|---|---|
| Walmart slogan "Save money. Live better." (real OCR) | Soccer goalposts on sports field |
| drive4walmart.com URL (real OCR) | Fire hydrant at street corner |
| "We're Hiring Drivers" sticker text | Pedestrian holding a sign near damaged barrier |
| Yale Ave. Exit 1 Mile sign | "County Road" on signboard (Ministral said "County Courthouse") |
| Trailer number "183539" | White metal chair leaning against tire stack |

Ministral has stronger OCR; devstral has stronger fine-scene-detail observation.

### Shared failure modes (neither model fixes)

- **Blurry / ambiguous images** — both models confabulate. Both hallucinated "a group of bicycles" on one blurred shot that contained neither bicycles nor a group.
- **Small text / text at angles** — Ministral substitutes wrong-but-real entities (saw "Hyundai Translead", wrote "Hyundai Transys" — a real but different Hyundai subsidiary). Devstral invents non-words ("Hyundai Transfusion" for the same text). Ministral's errors are slightly less harmful to the graph because "Transys" links to the real Hyundai corporate family; "Transfusion" is a pure phantom entity. Neither is reliable on small text.
- **Sport / activity identification without explicit cues** — Ministral mislabeled a soccer field as "volleyball or similar sports"; devstral caught "goalposts visible" and got it right. Devstral wins this category; it's one of the motivating reasons to run the augmentation pass.

## Context length trap when loading with `--parallel N`

**Most important operational pitfall.** When you load a model with `--parallel N` via `lms load`, LM Studio hard-partitions the context across slots: each slot gets `context / N` tokens. With the intuitive command:

```bash
lms load mistralai/ministral-3-3b --context-length 32000 --parallel 8
```

Each slot gets **4000 tokens** — which is **not enough** for LightRAG's entity-extraction prompts (observed ~4100–5100 total tokens per extraction call). The failure mode is sporadic: some chunks succeed, others return 400 Bad Request, correlated with per-chunk prompt size.

### Two working configurations

```bash
# A: oversize the context so per-slot budget is comfortable (CLI-accessible, what the validated setup uses)
lms load mistralai/ministral-3-3b --context-length 65536 --parallel 8
# 65536 / 8 = 8192 tokens per slot, comfortable headroom over LightRAG's ~5000-token extraction prompts

# B: enable Unified KV in the LM Studio UI (dynamic slot allocation, not CLI-exposed)
# Model load settings → advanced → Unified KV → on
# Slots share the pool and can use >fair-share when others are idle
```

Apply the same math when loading devstral for indexing or search. 3B models are cheap enough to oversize — 64k context on a 3B costs negligible extra memory. Larger models need more care:

```bash
lms load mistralai/devstral-small-2-2512 --context-length 32000 --parallel 4
# 32000 / 4 = 8000 per slot; 24B model so 32k is the ceiling before VRAM pressure
```

## LightRAG concurrency: GGUF + continuous batching

LM Studio's MLX engine does not support concurrent requests to a single loaded instance as of April 2026 ([lmstudio-ai/mlx-engine#203](https://github.com/lmstudio-ai/mlx-engine/issues/203)). Requests to one MLX instance are strictly serialized. LightRAG's 8-worker pool against MLX still provides pipeline saturation, CPU/GPU overlap, and parallel embedding calls — but **not** parallel LLM inference.

GGUF on llama.cpp flips the last row via continuous batching. With `Max Concurrent Predictions ≥ 8`, the 8 LightRAG workers each get a real parallel inference slot.

**Engine fingerprint via `lms ps`:**
- `PARALLEL=1` → MLX or GGUF without batching enabled
- `PARALLEL>1` → GGUF with continuous batching

For any LightRAG workload, `PARALLEL<8` is leaving throughput on the table. For LM Studio, GGUF is the right engine for everything except single-request latency-only workloads.

## M3 Ultra physical-GPU caveat

Loading two instances of the *same* model doubles VRAM but doesn't give 2× throughput — both instances share one GPU and contend for memory bandwidth. Observed on this hardware: two concurrent devstral instances each run at ~70% of solo speed. Aggregate throughput is ~1.4×, not 2×. The win is **progress concurrency** (indexing and describing advance simultaneously), not total throughput.

Loading **different** models simultaneously (Ministral for describe+index, devstral for search synthesis, nemotron for fast lookups, nomic for embeddings) is the right use of multi-model loading. Each serves a distinct job and per-instance latency matters more than aggregate throughput. All four can coexist on an M3 Ultra without memory pressure.

## Sanity-check queries after any re-index

```bash
# Concept query — should return multiple related photos via graph entities
./tools/search.sh --mode hybrid "Walmart truck with exit sign"

# Direct OCR literal — proves text made it from image → description → graph → retrieval
./tools/search.sh --mode naive "drive4walmart.com"

# Semantic bridging — query uses different words than any description contains
./tools/search.sh --mode hybrid "motorcyclists crossing a bridge"

# Multi-doc synthesis stress test — requires a big SEARCH_MODEL
SEARCH_MODEL="mistralai/devstral-small-2-2512" ./tools/search.sh --mode global "roadtrip in winter"
```

The fourth query is the synthesis-model check. If it cites only 1 photo on a corpus you know has many relevant shots, `SEARCH_MODEL` is too small.
