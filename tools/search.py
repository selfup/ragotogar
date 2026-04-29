#!/usr/bin/env python3
"""
Search photo descriptions via pgvector cosine similarity.

Requires an indexed corpus: run `cmd/describe` to populate the photos
tables, then `tools/index_and_vectorize.sh` to embed each description
into the chunks table.

Usage:
    python search.py "bedroom photos with warm light"
    python search.py --retrieve "shallow depth of field"
    python search.py --retrieve --verify "April photos with trees"

Environment:
    LIBRARY_DSN     (default: postgres:///ragotogar)
    LM_STUDIO_BASE  (default: http://localhost:1234)
    SEARCH_MODEL    (default: mistralai/ministral-3-3b) — LLM for verify
    EMBED_MODEL     (default: text-embedding-nomic-embed-text-v1.5)
"""

import argparse
import asyncio
import os
import sys

from rag_common import (
    LIBRARY_DSN, SEARCH_MODEL,
    build_document, connect_library, embed_texts, fetch_photo_dict, make_llm_func,
)


DEFAULT_TOP_K = 30
STRICT_TOP_K = 500
COSINE_THRESHOLD = 0.5  # applied in --retrieve and --precise modes


async def vector_search(conn, query, top_k, threshold=None):
    """Return [(name, similarity)] ordered by descending similarity.

    Aggregates per-photo: takes the best chunk score per photo so we don't
    return the same photo multiple times via different chunks. When
    `threshold` is set, only photos with similarity ≥ threshold come back —
    matches the old LightRAG `cosine_better_than_threshold` cutoff."""
    [embedding] = await embed_texts([query])
    rows = await conn.fetch("""
        SELECT name, MAX(1 - (embedding <=> $1)) AS similarity
        FROM chunks JOIN photos ON photos.id = chunks.photo_id
        GROUP BY name
        ORDER BY similarity DESC
        LIMIT $2
    """, embedding, top_k)
    out = [(r["name"], float(r["similarity"])) for r in rows]
    if threshold is not None:
        out = [(n, s) for n, s in out if s >= threshold]
    return out


def print_sources(results):
    """Print '[i] name' lines so cmd/web can parse them via the existing regex."""
    if not results:
        return
    print(f"\n--- Retrieved Sources ({len(results)} files) ---")
    for i, (name, _) in enumerate(results, 1):
        print(f"  [{i}] {name}")


VERIFY_PROMPT = """Determine if a photo is relevant to a search query.

Query: {query}

Photo data (camera, settings, date, software, photographer, and visual description):
{document}

If the data mentions or shows what the query is about — even as a small,
background, or partial element, or via metadata like camera/lens/date/settings —
answer YES. Only answer NO if the photo is clearly unrelated to the query.

Reply with exactly one word: YES or NO."""


async def _verify_one(query, name, document, llm_func):
    if not document:
        return name, False, "(no document)"
    prompt = VERIFY_PROMPT.format(query=query, document=document[:3000])
    try:
        resp = await llm_func(prompt)
    except Exception as e:
        print(f"  [verify error] {name}: {e}", file=sys.stderr)
        return name, False, f"(error: {e})"
    verdict = resp.strip().upper().startswith("Y")
    return name, verdict, resp.strip()


async def verify_filter(conn, query, candidates, llm_func):
    """Run parallel LLM verification on each candidate's indexed text."""
    documents = []
    for name, _ in candidates:
        data = await fetch_photo_dict(conn, name)
        documents.append(build_document(data) if data else None)

    print(f"\n--- Verifying {len(candidates)} candidate(s) with LLM ---", file=sys.stderr)
    results = await asyncio.gather(*[
        _verify_one(query, name, doc, llm_func)
        for (name, _), doc in zip(candidates, documents)
    ])
    kept = []
    for name, verdict, raw in results:
        marker = "✓" if verdict else "✗"
        print(f"  {marker} {name}: {raw[:80]}", file=sys.stderr)
        if verdict:
            kept.append(name)
    return kept


def print_verified(query, kept, total):
    print(f"\n--- Verified Sources ({len(kept)}/{total} kept) ---")
    for i, name in enumerate(kept, 1):
        print(f"  [{i}] {name}")


async def do_query(query_text, retrieve=False, precise=False, verify=False, dsn=None):
    conn = await connect_library(dsn)
    try:
        # Precision check: require some chunks to exist before querying.
        n = await conn.fetchval("SELECT COUNT(*) FROM chunks")
        if n == 0:
            print("No chunks in library. Run tools/index_and_vectorize.sh first.", file=sys.stderr)
            sys.exit(1)

        # Match the old LightRAG behavior: --retrieve and --precise both pin
        # cosine ≥ 0.5 with a wide top_k cap. Without this, every photo above
        # any similarity floods the verify pass — slow and noisy.
        if retrieve or precise:
            top_k = STRICT_TOP_K
            threshold = COSINE_THRESHOLD
        else:
            top_k = DEFAULT_TOP_K
            threshold = None
        results = await vector_search(conn, query_text, top_k, threshold=threshold)

        if verify and (retrieve or precise):
            kept = await verify_filter(conn, query_text, results, make_llm_func(SEARCH_MODEL))
            print_verified(query_text, kept, len(results))
        else:
            print_sources(results)
    finally:
        await conn.close()


def main():
    parser = argparse.ArgumentParser(description="Search photo descriptions via pgvector")
    parser.add_argument("text", help="Search query")
    group = parser.add_mutually_exclusive_group()
    group.add_argument("--retrieve", action="store_true",
                       help="Retrieval only — list matched photos, no LLM synthesis")
    group.add_argument("--precise", action="store_true",
                       help="Strict retrieval (cosine ≥ 0.5)")
    parser.add_argument("--verify", action="store_true",
                       help="With --retrieve: LLM yes/no check on each candidate, keep only YES matches")
    parser.add_argument("--dsn", default=LIBRARY_DSN,
                       help=f"Postgres DSN (default: {LIBRARY_DSN})")
    # Legacy --mode is accepted-but-ignored so existing call sites
    # (cmd/web/search.go) keep working without a flag-removal coordination.
    parser.add_argument("--mode", default=None, help=argparse.SUPPRESS)
    args = parser.parse_args()
    asyncio.run(do_query(
        args.text,
        retrieve=args.retrieve,
        precise=args.precise,
        verify=args.verify,
        dsn=args.dsn,
    ))


if __name__ == "__main__":
    main()
