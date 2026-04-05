#!/usr/bin/env python3
"""
Index photo description JSONs into a LightRAG knowledge graph.

Extracts entities/relationships via LLM and embeds text chunks for vector
search. Both steps happen in LightRAG's ainsert() call.

Usage:
    python index_and_vectorize.py /path/to/description_jsons
    python index_and_vectorize.py --reindex /path/to/description_jsons

Environment:
    LM_STUDIO_BASE  (default: http://localhost:1234)
    INDEX_MODEL     (default: devstral-small-2-2512) — LLM for entity extraction
    EMBED_MODEL     (default: text-embedding-nomic-embed-text-v1.5)
"""

import argparse
import asyncio
import json
import os
import shutil
from glob import glob

from rag_common import INDEX_DIR, INDEX_MODEL, EMBED_MODEL, LM_STUDIO_BASE, create_rag


def build_document(data):
    """Build a single text document from a photo description JSON for indexing.

    Combines metadata, structured fields, and full description into one
    document so LightRAG can extract entities and relationships across
    all the information.
    """
    parts = []

    # Photo identity
    parts.append(f"Photo: {data['name']}")
    parts.append(f"File: {data['file']}")

    # Camera metadata
    meta = data.get("metadata", {})
    if meta.get("make") or meta.get("model"):
        camera = f"{meta.get('make', '')} {meta.get('model', '')}".strip()
        parts.append(f"Camera: {camera}")
    if meta.get("date_time_original"):
        parts.append(f"Date: {meta['date_time_original']}")

    settings = []
    if meta.get("focal_length"):
        settings.append(meta["focal_length"])
    if meta.get("f_number"):
        settings.append(f"f/{meta['f_number']}")
    if meta.get("exposure_time"):
        settings.append(meta["exposure_time"])
    if meta.get("iso"):
        settings.append(f"ISO {meta['iso']}")
    if settings:
        parts.append(f"Settings: {', '.join(settings)}")

    if meta.get("flash"):
        parts.append(f"Flash: {meta['flash']}")

    # Full description (the main content for graph extraction)
    if data.get("description"):
        parts.append("")
        parts.append(data["description"])

    return "\n".join(parts)


async def do_index(json_dir, reindex=False):
    if reindex and os.path.exists(INDEX_DIR):
        print(f"Clearing existing index at {INDEX_DIR}")
        shutil.rmtree(INDEX_DIR)

    os.makedirs(INDEX_DIR, exist_ok=True)

    # Recursive glob so we pick up both flat layouts (descriptions/*.json) and
    # the hybrid Ministral+devstral layout (descriptions/ministral/*.json,
    # descriptions/devstral/*.json) when pointed at the parent directory.
    # See STRATEGIES.md for the hybrid indexing strategy.
    files = sorted(glob(os.path.join(json_dir, "**", "*.json"), recursive=True))
    if not files:
        print(f"No JSON files found in '{json_dir}'")
        return

    print(f"Found {len(files)} description(s) in '{json_dir}'")
    print(f"LLM:    {INDEX_MODEL} @ {LM_STUDIO_BASE}")
    print(f"Embed:  {EMBED_MODEL}")
    print(f"Index:  {INDEX_DIR}")
    print()

    rag = await create_rag()

    try:
        docs = []
        names = []
        for i, path in enumerate(files):
            name = os.path.basename(path)
            print(f"  [{i + 1}/{len(files)}] {name}")

            with open(path, "r") as f:
                data = json.load(f)

            docs.append(build_document(data))
            names.append(name)

        print(f"\nInserting {len(docs)} documents as batch...")
        await rag.ainsert(docs, file_paths=names)

        print(f"\nDone. Indexed {len(files)} documents.")
    finally:
        await rag.finalize_storages()


def main():
    parser = argparse.ArgumentParser(description="Index photo descriptions into LightRAG")
    parser.add_argument("json_dir", help="Directory containing .json description files")
    parser.add_argument("--reindex", action="store_true", help="Clear and rebuild the index")
    args = parser.parse_args()
    asyncio.run(do_index(args.json_dir, reindex=args.reindex))


if __name__ == "__main__":
    main()
