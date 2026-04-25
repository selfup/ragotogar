# Strategies

Operational choices that aren't obvious from the code alone — the *why* behind particular model selections, pipeline shapes, and trade-offs we've validated through testing. Update when a strategy changes or a new one is adopted.

## Quick reference

| Decision | Choice | Why |
|----------|--------|-----|
| Vision model | Qwen3-VL 8B | Best accuracy at 6.6s/photo — correctly IDs specific objects other models get wrong |
| Index/search LLM | Ministral 3B GGUF | Fast text-only task, real parallel batching with `--parallel 8` |
| Multi-doc synthesis | devstral 24B (override) | 3B cites 1 photo, 24B cites 9+ from the same retrieval context |
| Embeddings | nomic-embed-text-v1.5 | 768-dim, cosine scores top out ~0.5–0.6 — set threshold accordingly |
| Retrieval threshold | cosine ≥ 0.5 | Sweet spot for nomic: "airplanes" returns 1 correct result vs 69 noise at default 0.2 |
| Engine for LightRAG | GGUF (not MLX) | MLX serializes requests; GGUF gives real continuous batching for 8 workers |

## Three-slot model architecture

The RAG pipeline has three distinct LLM workloads, sized independently:

| Slot | Env var | Default |
|---|---|---|
| **Vision description** (`cmd/describe`) | `LM_MODEL` | `qwen/qwen3-vl-8b` |
| **Index entity extraction** (LightRAG ingest) | `INDEX_MODEL` | `mistralai/ministral-3-3b` |
| **Query synthesis** (LightRAG query) | `SEARCH_MODEL` | `mistralai/ministral-3-3b` |

Vision description is a batch workload where accuracy matters most — wrong entities poison the graph. See [Vision model selection](#vision-model-selection-qwen3-vl-8b) for the five-model comparison. Index extraction and query synthesis are text-only tasks where Ministral 3B's throughput and GGUF parallel batching are the main scaling levers (validated on 26-photo test set: 231 nodes / 231 edges, ~9 entities/photo, 2:52 wall-clock).

The trade-off — and the reason to know the SEARCH override exists — is multi-document synthesis quality.

### Known limitation: multi-document synthesis on small models

Same graph (231 nodes / 231 edges from Ministral-indexed photos), same retrieval (20 relevant chunks passed to the synthesis LLM), same query (`"roadtrip in winter"` in `global` mode):

- **Small model (4B)**: answer cites **1 photo** (the one with the strongest single-keyword match). Ignores 19 other relevant chunks.
- **devstral-small-2-2512 (24B GGUF)**: answer cites **9 photos** spanning the full March 21 road trip, organized into Road Conditions / Vehicle Prep / Route Planning / Safety Tips sections.

The retrieval pipeline surfaced identical context in both cases — only the model writing the final paragraph changed. Small models in the 3–4B range are genuinely limited at synthesizing across many chunks; they fixate on the chunk with the strongest surface-level match and ignore the rest. This is not an indexing problem, not a prompt problem, and not a retrieval problem — it's a raw model capacity ceiling.

### When to override `SEARCH_MODEL`

| Mode | Ministral 3B (default) | Override to 24B? |
|---|---|---|
| `naive` — direct vector retrieval | ✓ Fine | No |
| `local` — entity neighborhood | ✓ Usually fine | Only if answer feels incomplete |
| `global` — broad thematic answer | ✗ Cites too few photos | **Yes** |
| `hybrid` — local + global merged | ✗ Cites too few photos | **Yes** |
| `--precise` — strict retrieval + synthesis | ✓ Fine for <20 chunks | **Yes, for 20+ chunks** |

Per-query override:

```bash
SEARCH_MODEL="mistralai/devstral-small-2-2512" ./tools/search.sh --mode global "roadtrip in winter"
SEARCH_MODEL="mistralai/devstral-small-2-2512" ./tools/search.sh --precise "analyze the framing of every indoor photo"
```

You need devstral loaded in LM Studio for this to work. If you run the override command without the model loaded and LM Studio has JIT auto-load enabled, it'll load devstral on top of whatever else is running — which on an M3 Ultra is fine VRAM-wise but worth knowing about. Preloading avoids the surprise:

```bash
lms load mistralai/devstral-small-2-2512 --context-length 32000 --parallel 4
```

## Vision model selection: Qwen3-VL 8B

Qwen3-VL 8B replaced Ministral 3B as the default vision description model after a five-model comparison on identical B&W diner photos (vintage car interior scene). Previously, Ministral 3B was used for all three pipeline slots with optional devstral augmentation.

### Why Qwen3-VL 8B won

| Model | Size | Avg time | Vehicle ID | Hallucinations |
|---|---|---|---|---|
| **Qwen3-VL 8B** | 8B | 6.6s | "1959-1960 Chevrolet Bel Air" (correct) | None observed |
| **Ministral 3B** | 3B | 4.1s | "vintage-style car" (vague but safe) | Minor: generic descriptions |
| **Gemma 4 31B** | 31B | 12.2s | "pickup truck" (wrong) | Vehicle type |
| **Devstral 24B** | 24B | 15.8s | "1950s American sedan" (close) | "two people in backseat" (was one dummy in front seat) |
| **Qwen 2.5-VL 7B** | 7B | 17.5s | fabricated "Chevrolet" script text | Invented brand text on car |

Qwen3-VL 8B produces the richest correct entity set (specific make/model/year, soda fountain, side mirror reflections, "person wearing a white hat") at only 1.6× the latency of a 3B model. For RAG, "1960 Chevrolet Bel Air" is a far more useful graph node than "vintage car."

### Architecture note

Qwen3-VL 8B scores 69.6 MMMU — close to models 10× its size. It also scores 96.1 DocVQA and 94.4 ScreenSpot, indicating strong fine-detail perception. Ministral 3B scores 52.4 MMMU but uses the same 410M ViT vision encoder as the 24B Mistral — its "eyes" are identical to a much larger model, which explains why it's accurate despite low parameter count. The difference is that Qwen3-VL's language head is better at interpreting what the vision encoder sees without fabricating details.

### Pipeline

```bash
# Default: Qwen3-VL 8B
./scripts/batch_photo_describe.sh -output descriptions /Volumes/T9/X100VI/JPEG/March

# Or explicit
./scripts/batch_photo_describe.sh -model qwen/qwen3-vl-8b -output descriptions /Volumes/T9/X100VI/JPEG/March
```

~6.6s per photo. No augmentation pass needed — Qwen3-VL 8B catches fine scene details that previously required a separate devstral run.

### Ministral 3B for OCR-heavy scenes (optional)

Ministral 3B has stronger OCR on large clear text (Walmart slogan, URLs, sign text). For batches known to contain signage or text-heavy scenes, a Ministral pass can supplement:

```bash
./scripts/batch_photo_describe.sh \
  -output descriptions/ministral-ocr \
  -model mistralai/ministral-3-3b \
  /Volumes/T9/X100VI/JPEG/March
```

Index both directories — LightRAG merges overlapping entities automatically.

### Known failure modes

- **Blurry / ambiguous images** — all models confabulate. Both Ministral and devstral hallucinated "a group of bicycles" on one blurred shot that contained neither.
- **Small text / text at angles** — Ministral substitutes wrong-but-real entities ("Hyundai Transys" for "Hyundai Translead"). Qwen3-VL 8B has not been tested on these edge cases yet.
- **Larger models ≠ better** — Gemma 4 31B (12.2s) misidentified a sedan as a pickup truck. Qwen 2.5-VL 7B (17.5s) fabricated brand text. Parameter count does not predict vision accuracy for structured description tasks.
- **Repetition loops on visually repetitive scenes** — Scenes with many similar elements (rows of people in airport chairs, parking lots, shelves of identical items) can trigger degenerate output where the model repeats the same sentence hundreds of times until it exhausts the context window. Observed across multiple models (Qwen3-VL 8B, others) on a B&W airport gate photo (United gate B8, ~8 silhouetted figures in rows of chairs, backlit by floor-to-ceiling windows). The combination of heavy silhouettes, identical seating, and low foreground contrast leaves the model with nothing to differentiate figures, so it loops on "A person in a dark shirt sits near the center." Two mitigations are in place:

  1. **Prompt-level self-check** — the description prompt now opens with an instruction to notice repeating elements and summarize them as a group with a count, rather than enumerating each one. This is the primary fix and was validated on the airport gate photo — the model produced a clean grouped description with no looping.
  2. **Post-hoc repetition detection** — `detectRepetitionLoop()` in `cmd/describe/main.go` splits the response on sentence boundaries and flags any sentence (≥20 chars) that appears more than 5 times. Triggers a retry via the existing exponential backoff logic. This is the safety net for cases where the model ignores the prompt instruction.

## LM Studio operational pitfalls

Read this section before loading models. These are the things that will bite you.

### Context length trap with `--parallel N`

When you load a model with `--parallel N` via `lms load`, LM Studio hard-partitions the context across slots: each slot gets `context / N` tokens. With the intuitive command:

```bash
lms load mistralai/ministral-3-3b --context-length 32000 --parallel 8
```

Each slot gets **4000 tokens** — which is **not enough** for LightRAG's entity-extraction prompts (observed ~4100–5100 total tokens per extraction call). The failure mode is sporadic: some chunks succeed, others return 400 Bad Request, correlated with per-chunk prompt size.

**Two working configurations:**

```bash
# A: oversize the context so per-slot budget is comfortable (what the validated setup uses)
lms load mistralai/ministral-3-3b --context-length 65536 --parallel 8
# 65536 / 8 = 8192 tokens per slot, comfortable headroom over LightRAG's ~5000-token extraction prompts

# B: enable Unified KV in the LM Studio UI (dynamic slot allocation, not CLI-exposed)
# Model load settings → advanced → Unified KV → on
# Slots share the pool and can use >fair-share when others are idle
```

Apply the same math when loading devstral. 3B models are cheap enough to oversize — 64k context on a 3B costs negligible extra memory. Larger models need more care:

```bash
lms load mistralai/devstral-small-2-2512 --context-length 32000 --parallel 4
# 32000 / 4 = 8000 per slot; 24B model so 32k is the ceiling before VRAM pressure
```

### GGUF + continuous batching (not MLX)

LM Studio's MLX engine does not support concurrent requests to a single loaded instance as of April 2026 ([lmstudio-ai/mlx-engine#203](https://github.com/lmstudio-ai/mlx-engine/issues/203)). Requests to one MLX instance are strictly serialized. LightRAG's 8-worker pool against MLX still provides pipeline saturation, CPU/GPU overlap, and parallel embedding calls — but **not** parallel LLM inference.

GGUF on llama.cpp gives real continuous batching. With `Max Concurrent Predictions ≥ 8`, the 8 LightRAG workers each get a real parallel inference slot.

**Engine fingerprint via `lms ps`:**
- `PARALLEL=1` → MLX or GGUF without batching enabled
- `PARALLEL>1` → GGUF with continuous batching

For any LightRAG workload, `PARALLEL<8` is leaving throughput on the table. GGUF is the right engine for everything except single-request latency-only workloads.

### M3 Ultra multi-model loading

Loading two instances of the *same* model doubles VRAM but doesn't give 2× throughput — both instances share one GPU and contend for memory bandwidth. Observed: two concurrent devstral instances each run at ~70% of solo speed. Aggregate throughput is ~1.4×, not 2×. The win is **progress concurrency** (indexing and describing advance simultaneously), not total throughput.

Loading **different** models simultaneously (Ministral for describe+index, devstral for search synthesis, nomic for embeddings) is the right use of multi-model loading. Each serves a distinct job and per-instance latency matters more than aggregate throughput. All can coexist on an M3 Ultra without memory pressure.

## Retrieval tuning

### Cosine similarity threshold

The default `COSINE_THRESHOLD` in LightRAG is 0.2 — far too permissive for nomic-embed-text-v1.5. At 0.2, a query for "airplanes" returns 69 results, of which only 1 actually contains an airplane. The rest are semantically adjacent (sky, travel, vehicles, roads) but irrelevant.

| Threshold | "airplanes" results | "indoor" results | Notes |
|---|---|---|---|
| 0.2 (default) | 69 | 100+ | Mostly noise |
| 0.5 | **1 (correct)** | **63** | Sweet spot — high precision, no false positives on concrete nouns |
| 0.6+ | 0 | 0 | Too strict — nomic embeddings don't score this high |

Nomic embeddings top out around 0.5–0.6 cosine similarity even for strong matches. This is a property of the embedding model, not a bug.

`--retrieve` and `--precise` hardcode cosine ≥ 0.5 automatically but compose with `--mode` (naive/local/hybrid) — the strict threshold applies to whichever retrieval mode you pick. The default 0.2 is fine for synthesis queries — the LLM filters noise during synthesis, and wider retrieval gives it more material to work with.

### Why `naive` mode is better for retrieval

For concrete-noun queries like "airplanes", `naive` mode (pure vector search across all chunks) outperforms `hybrid` mode. Hybrid pre-filters through graph entity matching, which can exclude chunks whose text mentions airplanes but whose entities weren't linked to an "airplane" graph node. Tested: `naive` returned 69 candidates at default threshold vs `hybrid`'s 41 — the graph acted as an accidental filter that dropped real matches.

This holds for *conceptual* queries too on small corpora (validated on 41-photo April set, queries like "planes in sky"): `naive` returned the right photos, `local` underperformed because entity-extraction depth on a 41-doc graph isn't dense enough to back graph-walking. `cmd/web` ships `naive` as the default mode for this reason; the toggle exists so you can A/B against `local`/`hybrid` if a query feels like it should benefit from graph traversal.

`--retrieve` and `--precise` compose with `--mode` — the strict cosine threshold applies regardless of the retrieval mode you pick. Graph modes add value for synthesis where structured context matters.

### `--precise` mode

`--precise` does strict retrieval (cosine ≥ 0.5, naive, all matches) then synthesizes over only exact matches. The result set can be large — e.g. "indoor" returns 65 photos from a 477-doc corpus. Model choice matters:

Tested on `"analyze the framing of every indoor photo"` (65 retrieved chunks):

- **Ministral 3B**: analyzed 33 photos, cited 15 in references. Identified core patterns correctly (low angle, shallow depth of field, leading lines). Faster, sufficient for smaller result sets.
- **devstral 24B**: analyzed 62/65 photos, correctly flagged 3 as non-indoor. Near-exhaustive coverage.

Rule of thumb: **<20 chunks** → Ministral 3B is fine. **20+ chunks** → override to devstral for exhaustive analysis.

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
