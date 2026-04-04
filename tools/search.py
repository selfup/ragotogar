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


async def do_query(query_text, mode="hybrid"):
    if not os.path.exists(INDEX_DIR):
        print("No index found. Run index_and_vectorize.py first.", file=sys.stderr)
        sys.exit(1)

    rag = await create_rag(model=SEARCH_MODEL)

    try:
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
    args = parser.parse_args()
    asyncio.run(do_query(args.text, mode=args.mode))


if __name__ == "__main__":
    main()
