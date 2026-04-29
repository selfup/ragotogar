#!/usr/bin/env python3
"""
Index photo descriptions from the SQL library into pgvector.

Reads photos / exif / descriptions from Postgres, chunks the build_document()
output, embeds each chunk via LM Studio, and INSERTs into the chunks table.
No entity extraction, no graph store — pgvector handles similarity directly.

Usage:
    python index_and_vectorize.py
    python index_and_vectorize.py --reindex
    python index_and_vectorize.py --dsn postgres:///other_db

Environment:
    LIBRARY_DSN     (default: postgres:///ragotogar)
    LM_STUDIO_BASE  (default: http://localhost:1234)
    EMBED_MODEL     (default: text-embedding-nomic-embed-text-v1.5)
"""

import argparse
import asyncio

from rag_common import (
    EMBED_MODEL, LM_STUDIO_BASE, LIBRARY_DSN,
    build_document, chunk_text, connect_library, embed_texts, fetch_photo_dict,
)


async def index_one(conn, name, *, replace=True):
    """Chunk + embed a single photo and write its rows to `chunks`.

    With replace=True (default for --reindex and re-indexing), deletes any
    existing chunks for the photo first, so re-runs are idempotent.
    Returns the number of chunks written."""
    data = await fetch_photo_dict(conn, name)
    if data is None:
        return 0
    doc = build_document(data)
    chunks = chunk_text(doc)
    if not chunks:
        return 0

    embeddings = await embed_texts(chunks)
    async with conn.transaction():
        if replace:
            await conn.execute("DELETE FROM chunks WHERE photo_id = $1", name)
        await conn.executemany(
            "INSERT INTO chunks (photo_id, idx, text, embedding) VALUES ($1, $2, $3, $4)",
            [(name, i, t, e) for i, (t, e) in enumerate(zip(chunks, embeddings))],
        )
    return len(chunks)


async def do_index(dsn, reindex=False):
    conn = await connect_library(dsn)
    try:
        if reindex:
            await conn.execute("TRUNCATE chunks")
            print("Truncated chunks table")

        names = [r["name"] for r in await conn.fetch("SELECT name FROM photos ORDER BY name")]
        if not names:
            print(f"No photos in {dsn}. Run cmd/describe first.")
            return

        # When --reindex truncated, every photo is missing; otherwise skip
        # photos that already have any chunks (cheap incremental top-up).
        existing = set()
        if not reindex:
            existing = {r["photo_id"] for r in await conn.fetch(
                "SELECT DISTINCT photo_id FROM chunks"
            )}

        todo = [n for n in names if n not in existing]
        skipped = len(names) - len(todo)

        print(f"Found {len(names)} photo(s) in {dsn} (skipping {skipped} already indexed)")
        print(f"Embed: {EMBED_MODEL} @ {LM_STUDIO_BASE}")
        print()

        total_chunks = 0
        for i, name in enumerate(todo, 1):
            n = await index_one(conn, name)
            total_chunks += n
            if i % 10 == 0 or i == len(todo):
                print(f"  [{i}/{len(todo)}] {name} ({n} chunks; {total_chunks} total)")

        print(f"\nDone. Indexed {len(todo)} photo(s), {total_chunks} chunk(s).")
    finally:
        await conn.close()


def main():
    ap = argparse.ArgumentParser(description="Index photo descriptions from the SQL library into pgvector")
    ap.add_argument("--dsn", default=LIBRARY_DSN, help=f"Postgres DSN (default: {LIBRARY_DSN})")
    ap.add_argument("--reindex", action="store_true", help="Truncate chunks before re-embedding all photos")
    args = ap.parse_args()
    asyncio.run(do_index(args.dsn, reindex=args.reindex))


if __name__ == "__main__":
    main()
