#!/usr/bin/env python3
"""
Search photo descriptions using a LightRAG knowledge graph.

Requires an existing index built by index_and_vectorize.py.

Usage:
    python search.py "bedroom photos with warm light"
    python search.py --mode naive "shallow depth of field"
    python search.py --mode local "what cameras were used"
    python search.py --mode global "summarize all indoor scenes"

Environment:
    LM_STUDIO_BASE  (default: http://localhost:1234)
    INDEX_MODEL     (default: devstral-small-2-2512) — LLM for query reasoning
    EMBED_MODEL     (default: text-embedding-nomic-embed-text-v1.5)
"""

import argparse
import asyncio
import json
import os
import sys

from lightrag import QueryParam

from rag_common import INDEX_DIR, SEARCH_MODEL, build_document, create_rag, make_llm_func


def unique_files(data):
    """Extract unique source file paths from a query-data response, in retrieval order."""
    refs = data.get("references", [])
    chunks = data.get("chunks", [])
    ref_map = {r["reference_id"]: r["file_path"] for r in refs if "reference_id" in r}

    seen = set()
    files = []
    for chunk in chunks:
        fp = chunk.get("file_path") or ref_map.get(chunk.get("reference_id"), "")
        if fp and fp not in seen:
            seen.add(fp)
            files.append(fp)
    for r in refs:
        fp = r.get("file_path", "")
        if fp and fp not in seen:
            seen.add(fp)
            files.append(fp)
    return files


def print_sources(data):
    """Print retrieved source files from structured query data."""
    files = unique_files(data)
    if not files:
        return
    print(f"\n--- Retrieved Sources ({len(files)} files) ---")
    for i, fp in enumerate(files, 1):
        print(f"  [{i}] {fp}")


def _read_indexable_text(json_path, json_dir=None):
    """Read the same indexable representation that index_and_vectorize.py used.

    Verify must see the same text the indexer embedded so retrieval and
    verification stay coherent (a query for "April" or "X100VI" or "f/2"
    matches both layers or neither). LightRAG stores only basenames, so when
    the indexed path doesn't resolve from cwd we fall back to <json_dir>/<basename>.
    Returns None if neither lookup succeeds.
    """
    paths_to_try = [json_path]
    if json_dir:
        paths_to_try.append(os.path.join(json_dir, os.path.basename(json_path)))
    for p in paths_to_try:
        try:
            with open(p, "r") as f:
                data = json.load(f)
            return build_document(data)
        except FileNotFoundError:
            continue
        except Exception:
            return None
    return None


VERIFY_PROMPT = """Determine if a photo is relevant to a search query.

Query: {query}

Photo data (camera, settings, date, software, photographer, and visual description):
{document}

If the data mentions or shows what the query is about — even as a small,
background, or partial element, or via metadata like camera/lens/date/settings —
answer YES. Only answer NO if the photo is clearly unrelated to the query.

Reply with exactly one word: YES or NO."""


async def _verify_one(query, file_path, document, llm_func):
    """Ask the LLM if a photo matches the query. Returns (file_path, verdict, raw_response)."""
    if not document:
        return file_path, False, "(no document)"
    prompt = VERIFY_PROMPT.format(query=query, document=document[:3000])
    try:
        resp = await llm_func(prompt)
    except Exception as e:
        print(f"  [verify error] {file_path}: {e}", file=sys.stderr)
        return file_path, False, f"(error: {e})"
    verdict = resp.strip().upper().startswith("Y")
    return file_path, verdict, resp.strip()


async def verify_filter(query, files, llm_func, json_dir=None):
    """Run parallel LLM verification on each candidate's indexed text, return only matches.
    Logs per-photo verdicts to stderr for debugging."""
    documents = [_read_indexable_text(fp, json_dir) for fp in files]
    print(f"\n--- Verifying {len(files)} candidate(s) with LLM ---", file=sys.stderr)
    results = await asyncio.gather(*[
        _verify_one(query, fp, doc, llm_func) for fp, doc in zip(files, documents)
    ])
    kept = []
    for fp, verdict, raw in results:
        marker = "✓" if verdict else "✗"
        print(f"  {marker} {os.path.basename(fp)}: {raw[:80]}", file=sys.stderr)
        if verdict:
            kept.append(fp)
    return kept


def print_verified(query, kept, total):
    print(f"\n--- Verified Sources ({len(kept)}/{total} kept) ---")
    for i, fp in enumerate(kept, 1):
        print(f"  [{i}] {fp}")


async def do_query(query_text, mode="hybrid", sources=False, retrieve=False, precise=False, verify=False, json_dir=None):
    if not os.path.exists(INDEX_DIR):
        print("No index found. Run index_and_vectorize.py first.", file=sys.stderr)
        sys.exit(1)

    cosine_threshold = 0.5 if (precise or retrieve) else None
    rag = await create_rag(model=SEARCH_MODEL, cosine_threshold=cosine_threshold)

    # --retrieve and --precise both pin chunk_top_k=500; the user's chosen --mode
    # (naive/local/hybrid) is preserved so retrieval can be either pure vector
    # or graph-aware.
    strict_top_k = 500

    try:
        if retrieve:
            result = await rag.aquery_data(query_text, param=QueryParam(mode=mode, enable_rerank=False, chunk_top_k=strict_top_k))
            if verify:
                files = unique_files(result.get("data", {}))
                kept = await verify_filter(query_text, files, make_llm_func(SEARCH_MODEL), json_dir=json_dir)
                print_verified(query_text, kept, len(files))
            else:
                print_sources(result.get("data", {}))
        elif precise:
            result = await rag.aquery_llm(query_text, param=QueryParam(mode=mode, enable_rerank=False, chunk_top_k=strict_top_k))
            print(result.get("llm_response", {}).get("content", ""))
            print_sources(result.get("data", {}))
        elif sources:
            result = await rag.aquery_llm(query_text, param=QueryParam(mode=mode, enable_rerank=False))
            print(result.get("llm_response", {}).get("content", ""))
            print_sources(result.get("data", {}))
        else:
            result = await rag.aquery(query_text, param=QueryParam(mode=mode, enable_rerank=False))
            print(result)
    finally:
        await rag.finalize_storages()


def main():
    parser = argparse.ArgumentParser(description="Search photo descriptions via LightRAG")
    parser.add_argument("text", help="Search query")
    parser.add_argument(
        "--mode",
        choices=["naive", "local", "global", "hybrid"],
        default="hybrid",
        help="Query mode (default: hybrid)",
    )
    group = parser.add_mutually_exclusive_group()
    group.add_argument(
        "--sources",
        action="store_true",
        help="Show all retrieved source files after the synthesis",
    )
    group.add_argument(
        "--retrieve",
        action="store_true",
        help="Retrieval only — list matched source files, no LLM synthesis",
    )
    group.add_argument(
        "--precise",
        action="store_true",
        help="Strict retrieval (cosine>=0.5, naive) then synthesize over exact matches only",
    )
    parser.add_argument(
        "--verify",
        action="store_true",
        help="With --retrieve: run an LLM yes/no check on each candidate's description, keep only YES matches",
    )
    parser.add_argument(
        "--json-dir",
        default=None,
        help="Directory containing the photo .json files; used by --verify to resolve LightRAG basenames to readable paths",
    )
    args = parser.parse_args()
    asyncio.run(do_query(args.text, mode=args.mode, sources=args.sources, retrieve=args.retrieve, precise=args.precise, verify=args.verify, json_dir=args.json_dir))


if __name__ == "__main__":
    main()
