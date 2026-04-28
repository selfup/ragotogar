#!/usr/bin/env python3
"""
Index photo descriptions from the SQL library into a LightRAG knowledge graph.

Reads photos / exif / descriptions from tools/.sql_index/library.db (populated
by cmd/describe) and feeds each row through build_document() into LightRAG.
Both entity extraction and embedding happen inside ainsert().

Usage:
    python index_and_vectorize.py
    python index_and_vectorize.py --reindex
    python index_and_vectorize.py --db /path/to/other.db

Environment:
    LM_STUDIO_BASE  (default: http://localhost:1234)
    INDEX_MODEL     (default: mistralai/ministral-3-3b)
    EMBED_MODEL     (default: text-embedding-nomic-embed-text-v1.5)
"""

import argparse
import asyncio
import os
import shutil

from rag_common import (
    INDEX_DIR, INDEX_MODEL, EMBED_MODEL, LM_STUDIO_BASE,
    LIBRARY_DB, build_document, connect_library, create_rag,
    fetch_photo_dict, iter_photo_names,
)


async def do_index(db_path, reindex=False):
    if reindex and os.path.exists(INDEX_DIR):
        print(f"Clearing existing index at {INDEX_DIR}")
        shutil.rmtree(INDEX_DIR)

    os.makedirs(INDEX_DIR, exist_ok=True)

    conn = connect_library(db_path)
    names = list(iter_photo_names(conn))
    if not names:
        print(f"No photos in {db_path}. Run cmd/describe first.")
        conn.close()
        return

    print(f"Found {len(names)} photo(s) in {db_path}")
    print(f"LLM:    {INDEX_MODEL} @ {LM_STUDIO_BASE}")
    print(f"Embed:  {EMBED_MODEL}")
    print(f"Index:  {INDEX_DIR}")
    print()

    rag = await create_rag()

    try:
        docs = []
        for i, name in enumerate(names):
            print(f"  [{i + 1}/{len(names)}] {name}")
            data = fetch_photo_dict(conn, name)
            if data is None:
                print(f"    [skip] {name} disappeared between SELECT and fetch")
                continue
            docs.append(build_document(data))

        print(f"\nInserting {len(docs)} documents as batch...")
        await rag.ainsert(docs, file_paths=names)

        print(f"\nDone. Indexed {len(docs)} documents.")
    finally:
        conn.close()
        await rag.finalize_storages()


def main():
    parser = argparse.ArgumentParser(description="Index photo descriptions from the SQL library into LightRAG")
    parser.add_argument("--db", default=LIBRARY_DB, help=f"SQLite library path (default: {LIBRARY_DB})")
    parser.add_argument("--reindex", action="store_true", help="Clear and rebuild the LightRAG index")
    args = parser.parse_args()
    asyncio.run(do_index(args.db, reindex=args.reindex))


if __name__ == "__main__":
    main()
