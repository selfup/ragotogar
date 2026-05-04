# Search skill

How to write effective queries against the ragotogar photo library.

Companion reference to the [Query syntax](../README.md#query-syntax) section in the README — that's the operator manual; this is the playbook.

## Pick the right mode

| Mode | Use when |
|------|----------|
| `vector` | Default. Pure semantic match — sub-second. Best for shape-of-the-photo queries (`bedroom with warm light`, `wide mountain shot`). Returns everything above the cosine floor. |
| `vector+verify` | When `vector` returns too much shape-but-wrong-content noise. The LLM judges each candidate against the query text. ~1–6s. |
| `FTS+vector` | When the query has metadata literals (`X100VI`, `2024`) **or** you want boolean operators. RRF fuses vector + FTS lanes. |
| `FTS+vector+verify` | Tightest precision, slowest. Save for when the boolean toolkit isn't enough. |
| `auto` | When you want to type natural language and have an LLM rewrite it into the boolean form for you. Adds ~250-500ms LLM latency for the rewrite. The rewritten query is shown above results so you can see what ran. Off by default; tick `save rewrite` to cache once you're satisfied. |
| `auto+verify` | Auto-rewrite + per-candidate prose verify. Tightest auto path. |

## Toggles (compose with any mode)

| Toggle | What it does |
|--------|--------------|
| `classifier filter` | After retrieval, an LLM compares the user's NL query against each candidate's classifier verdict (typed enums like `subject_altitude=in_air`, `scene_weather=overcast`) and drops contradictions. Catches the prose-vs-verdict gap (a sky-plane photo whose prose mentions "no taxiway visible" gets dropped because the classifier verdict is `subject_altitude=in_air`). One batched LLM call per query. |
| `save rewrite` | Caches the auto-mode rewrite. Off by default — leave off while iterating to a good rewrite, tick once happy. |
| `save classifier filter` | Caches classifier filter verdicts per `(NL query, photo, model)`. Same iterate-then-save pattern. Re-classifying a photo silently invalidates older verdicts. |

## Operators (cheat sheet)

| Syntax | Meaning |
|--------|---------|
| `red truck` | bare AND — both lexemes required, anywhere in indexed text |
| `"red truck"` | phrase — adjacent in order. **Use for attribute binding.** |
| `red OR maroon` | disjunction (uppercase `OR`; lowercase `or` is a stopword) |
| `-truck` | exclude photos whose prose stem-matches `truck` |
| `-"black and white"` | exclude phrase |

Stopwords (`AND`, `the`, `on`, `is`, `a`, …) are silently dropped during parsing. `planes AND aircraft` is identical to `planes aircraft` — bare terms already AND.

Negation reaches both arms — FTS via `websearch_to_tsquery` natively, vector via `library.StripNegation` (clean embed input) + post-filter against the same FTS surface using `library.ExtractNegation`.

## Patterns that work

### 1. Phrase-bind attribute pairs

The vector arm finds geometric shape regardless of prose ("dashcam highway truck" matches whatever's geometrically dashcam-shaped). The FTS arm bare-AND's lexemes anywhere — so `red truck` matches "red brake lights … truck on road" the same as a real red truck.

Quoted phrases force adjacency in the prose. This is the single biggest precision lever.

```
"red truck" on road
"aircraft on taxiways" "aircraft on the ground"
"plane in flight" -"on the ground"
```

### 2. Stack negations against describer vocabulary

The describer doesn't always use the canonical word. To exclude clouds, one `-cloud` isn't enough — Qwen3-VL writes "overcast", "scattered cumulus", "puffy white", "wispy", "blanket of cloud cover", etc.

Build the negation set by inspecting what the describer actually wrote for a slipping photo:

```sql
SELECT colors, light, full_description
FROM descriptions d JOIN photos p ON p.id = d.photo_id
WHERE p.name = '<filename of an unwanted result>';
```

Then pile on the tokens you find:

```
planes on taxiways -cloud -clouds -cloudy -overcast -cumulus -puffy -wispy -scattered
```

### 3. Tune the sliders

| Slider | Effect | Default | Notes |
|--------|--------|---------|-------|
| `cosine ≥` | flat absolute floor on vector similarity | `0.50` | Raise to 0.55–0.60 to drop semantic-collision noise (parking lots ranking near "planes parked"). Lower if pure `vector` mode returns 0. |
| `fts ≥` | relative floor — `value × max(ts_rank)` for the query | `0.30` | Adaptive. Raise (0.70+) when only the top few FTS hits should survive into RRF. |

When pure `vector` mode returns 0 but `FTS+vector` returns plenty — that's the flat cosine floor pruning the long tail, not a vector failure. Drop the slider before assuming a bug.

## Examples from real iterations

| Query | Result | What changed |
|-------|--------|--------------|
| `red truck on road` | 418 hits, lots of B&W highway shots | bare AND — `red brake lights + truck + road` matches |
| `red truck on road -monochrome` | 344 | catches photos whose prose says "monochrome" only |
| `red truck on road -monochrome -"black and white" -grayscale -desaturated` | narrower | covers more of the B&W vocabulary tail |
| `"red truck" on road -monochrome -"black and white"` | tight | phrase binding eliminates "red brake lights + truck" entirely |
| `planes on taxiways on the ground -cloud` | 76 | only catches prose with literal `cloud` stem |
| `planes on taxiways on the ground -cloud -overcast -cumulus -cloudy` | 45 | broader vocabulary; some still leak |
| `planes "aircraft on taxiways" "aircraft on the ground" -car -vehicle -flying -"in the air"` | 86, all clear | phrase-bound + targeted negations — clean recall, includes cloudy skies as desired |

## Known gaps

- **Classifier enums aren't reachable from FTS yet.** `classified.scene_weather = 'overcast'`, `pov_container = 'in_plane'`, etc. are written into `chunks.text` via `library.BuildDocument` (so the vector lane sees them) but **not** into `descriptions.fts` or `exif.fts`. So `-overcast` only catches photos whose prose stem-matches `overcast`, not photos the classifier verdict labeled overcast. Future fix: a `classified.fts` generated column on the same shape as `exif.fts`. Vocabulary at `library/classify.go:74`.
- **No numerical / range filters.** `f/2.8 or wider`, `April 2024 only`, `ISO ≥ 1600` — pgvector + FTS can't express these. See `ARCHITECTURE.md` Phase 7 for the LLM-parse sketch.
- **Vector arm ignores positive operators.** `OR` and quoted phrases pass through to the embedder as text — it reads `"red truck"` as literally those characters, not as a phrase constraint. Negation has the post-filter; positive operators rely on FTS for binding semantics. Phrase-bind in `FTS+vector` modes; in pure `vector` mode the quotes are cosmetic.
- **Compound dashed words survive negation parsing.** `truck-driver -truck` keeps `truck-driver` as one token (not stripped) and only excludes the standalone `-truck` lexeme — the embedder still sees the compound.

## Quick recipe

For the typical "photos of X doing Y, not Z":

1. Start in **`vector`** mode with the plain natural-language query.
2. Too noisy? Two paths:
   - **`FTS+vector`** — type your own boolean: phrase-bind `"X doing Y"`, stack `-tokens` for unwanted vocabulary.
   - **`auto`** — type the natural-language version, the LLM rewrites it for you. Show-up of what ran is in the rewrite line above results. Tick `save rewrite` when satisfied.
3. Photos with the right *prose* but wrong *content* slipping through (the prose mentioned what's NOT in the frame, e.g. "no taxiway visible")? Tick **`classifier filter`**. Drops candidates whose typed classifier verdict contradicts the NL query — one extra LLM call, ~500ms-1s.
4. Still noisy because of geometric/shape collisions (vehicles ≈ planes, etc.)? **Raise the `cosine ≥` slider** — vector lane is the source.
5. Last resort: **`+verify`** mode (any of the verify variants) for per-candidate LLM judgment. Slow but tight.

## Anti-patterns

- **Don't rely on `-` alone as a token.** `red truck -` won't parse as a negation — the dash with no following term is left untouched. Negation needs `-term` or `-"phrase"`, no space after the dash.
- **Don't quote single words.** `-"flying"` is identical to `-flying` — phrase semantics need 2+ tokens.
- **Don't expect `vector` mode to honor quotes.** Pure vector embedding doesn't parse phrase syntax. Use `FTS+vector` modes for boolean.
- **Don't conflate `cosine ≥` with `fts ≥`.** Same control type, different semantics — flat vs. adaptive. Doubling one doesn't double the other's effect.
