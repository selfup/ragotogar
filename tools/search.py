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
import os
import sys

from lightrag import QueryParam

from rag_common import INDEX_DIR, SEARCH_MODEL, create_rag


def print_sources(data):
    """Print retrieved source files from structured query data."""
    refs = data.get("references", [])
    chunks = data.get("chunks", [])
    if not refs and not chunks:
        return

    # Build ref_id -> file_path lookup
    ref_map = {r["reference_id"]: r["file_path"] for r in refs if "reference_id" in r}

    # Collect unique file paths in retrieval order
    seen = set()
    files = []
    for chunk in chunks:
        fp = chunk.get("file_path") or ref_map.get(chunk.get("reference_id"), "")
        if fp and fp not in seen:
            seen.add(fp)
            files.append(fp)
    # Pick up any refs not already covered by chunks
    for r in refs:
        fp = r.get("file_path", "")
        if fp and fp not in seen:
            seen.add(fp)
            files.append(fp)

    print(f"\n--- Retrieved Sources ({len(files)} files) ---")
    for i, fp in enumerate(files, 1):
        print(f"  [{i}] {fp}")


async def do_query(query_text, mode="hybrid", sources=False, retrieve=False):
    if not os.path.exists(INDEX_DIR):
        print("No index found. Run index_and_vectorize.py first.", file=sys.stderr)
        sys.exit(1)

    rag = await create_rag(model=SEARCH_MODEL)

    try:
        if retrieve:
            result = await rag.aquery_data(query_text, param=QueryParam(mode=mode, enable_rerank=False))
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
    args = parser.parse_args()
    asyncio.run(do_query(args.text, mode=args.mode, sources=args.sources, retrieve=args.retrieve))


if __name__ == "__main__":
    main()
