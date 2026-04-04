#!/usr/bin/env python3
"""
Photo description search using LightRAG.

Indexes JSON photo descriptions into a knowledge graph and provides
semantic + graph-based search across your photo library.

Usage:
    # Index descriptions
    python search.py index /path/to/description_jsons

    # Query (hybrid mode - best results)
    python search.py query "bedroom photos with warm light"

    # Query with specific mode
    python search.py query --mode naive "shallow depth of field"
    python search.py query --mode local "what cameras were used"
    python search.py query --mode global "summarize all indoor scenes"

    # Re-index (clear existing graph and rebuild)
    python search.py index --reindex /path/to/description_jsons

Environment:
    LM_STUDIO_BASE  (default: http://localhost:1234)
    LM_MODEL        (default: qwen3.5-35b-a3b)
    EMBED_MODEL     (default: text-embedding-nomic-embed-text-v1.5)
"""

import argparse
import asyncio
import json
import os
import shutil
import sys
from functools import partial
from glob import glob

import re

import numpy as np
from openai import AsyncOpenAI

from lightrag import LightRAG, QueryParam
from lightrag.utils import EmbeddingFunc

THINK_RE = re.compile(r"<think>.*?</think>", re.DOTALL)

SCRIPT_DIR = os.path.dirname(os.path.abspath(__file__))
INDEX_DIR = os.path.join(SCRIPT_DIR, ".rag_index")

LM_STUDIO_BASE = os.environ.get("LM_STUDIO_BASE", "http://localhost:1234")
LM_MODEL = os.environ.get("LM_MODEL", "qwen/qwen3.5-35b-a3b")
EMBED_MODEL = os.environ.get("EMBED_MODEL", "text-embedding-nomic-embed-text-v1.5")


async def llm_func(prompt, system_prompt=None, history_messages=[], **kwargs):
    """Call LM Studio LLM, handling reasoning models that split thinking into a separate field."""
    import httpx

    messages = []
    if system_prompt:
        messages.append({"role": "system", "content": system_prompt})
    if history_messages:
        messages.extend(history_messages)
    messages.append({"role": "user", "content": prompt})

    payload = {
        "model": LM_MODEL,
        "messages": messages,
        "max_tokens": -1,
        "temperature": 0.0,
    }

    async with httpx.AsyncClient(timeout=300) as client:
        resp = await client.post(
            f"{LM_STUDIO_BASE}/v1/chat/completions",
            json=payload,
            headers={"Authorization": "Bearer lm-studio"},
        )
        resp.raise_for_status()

    data = resp.json()
    content = (data["choices"][0]["message"]["content"] or "").strip()
    # Strip any <think> blocks that might appear inline
    content = THINK_RE.sub("", content).strip()
    finish = data["choices"][0].get("finish_reason", "unknown")
    usage = data.get("usage", {})
    reasoning_len = len(data["choices"][0]["message"].get("reasoning_content", ""))
    print(f"  [llm] input={len(prompt)}chars output={len(content)}chars finish={finish} reasoning={reasoning_len}chars usage={usage}", file=sys.stderr, flush=True)
    if not content:
        raise ValueError("LLM returned empty content")
    return content


async def embed_func(texts: list[str], **kwargs) -> np.ndarray:
    """Embed texts via LM Studio without sending the dimensions parameter."""
    client = AsyncOpenAI(base_url=f"{LM_STUDIO_BASE}/v1", api_key="lm-studio")
    resp = await client.embeddings.create(model=EMBED_MODEL, input=texts)
    return np.array([d.embedding for d in resp.data])


async def create_rag():
    rag = LightRAG(
        working_dir=INDEX_DIR,
        llm_model_func=llm_func,
        llm_model_max_async=1,  # sequential — LM Studio can't handle concurrent LLM requests
        max_extract_input_tokens=20480,
        embedding_func=EmbeddingFunc(
            embedding_dim=768,  # nomic-embed-text-v1.5 output dimension
            max_token_size=8192,
            func=embed_func,
        ),
        chunk_token_size=1200,
        chunk_overlap_token_size=100,
        embedding_batch_num=8,
    )
    await rag.initialize_storages()
    return rag


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

    files = sorted(glob(os.path.join(json_dir, "*.json")))
    if not files:
        print(f"No JSON files found in '{json_dir}'")
        return

    print(f"Found {len(files)} description(s) in '{json_dir}'")
    print(f"LLM:    {LM_MODEL} @ {LM_STUDIO_BASE}")
    print(f"Embed:  {EMBED_MODEL}")
    print(f"Index:  {INDEX_DIR}")
    print()

    rag = await create_rag()

    try:
        for i, path in enumerate(files):
            name = os.path.basename(path)
            print(f"  [{i + 1}/{len(files)}] {name}")

            with open(path, "r") as f:
                data = json.load(f)

            doc = build_document(data)
            await rag.ainsert(doc)

        print(f"\nDone. Indexed {len(files)} documents.")
    finally:
        await rag.finalize_storages()


async def do_query(query_text, mode="hybrid"):
    if not os.path.exists(INDEX_DIR):
        print("No index found. Run 'index' first.", file=sys.stderr)
        sys.exit(1)

    rag = await create_rag()

    try:
        result = await rag.aquery(query_text, param=QueryParam(mode=mode))
        print(result)
    finally:
        await rag.finalize_storages()


def main():
    parser = argparse.ArgumentParser(description="Photo description search with LightRAG")
    sub = parser.add_subparsers(dest="command")

    idx = sub.add_parser("index", help="Index photo description JSONs")
    idx.add_argument("json_dir", help="Directory containing .json description files")
    idx.add_argument("--reindex", action="store_true", help="Clear and rebuild the index")

    qry = sub.add_parser("query", help="Search indexed descriptions")
    qry.add_argument("text", help="Search query")
    qry.add_argument(
        "--mode",
        choices=["naive", "local", "global", "hybrid"],
        default="hybrid",
        help="Query mode (default: hybrid)",
    )

    args = parser.parse_args()

    if args.command == "index":
        asyncio.run(do_index(args.json_dir, reindex=args.reindex))
    elif args.command == "query":
        asyncio.run(do_query(args.text, mode=args.mode))
    else:
        parser.print_help()
        sys.exit(1)


if __name__ == "__main__":
    main()
