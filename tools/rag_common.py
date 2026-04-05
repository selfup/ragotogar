"""
Shared LightRAG configuration and helper functions.

Used by index_and_vectorize.py and search.py.
"""

import os
import re
import sys

import numpy as np
from openai import AsyncOpenAI

from lightrag import LightRAG
from lightrag.utils import EmbeddingFunc

THINK_RE = re.compile(r"<think>.*?</think>", re.DOTALL)

SCRIPT_DIR = os.path.dirname(os.path.abspath(__file__))
INDEX_DIR = os.path.join(SCRIPT_DIR, ".rag_index")

LM_STUDIO_BASE = os.environ.get("LM_STUDIO_BASE", "http://localhost:1234")
INDEX_MODEL = os.environ.get("INDEX_MODEL", "mistralai/devstral-small-2-2512")
SEARCH_MODEL = os.environ.get("SEARCH_MODEL", "nvidia/nemotron-3-nano-4b")
EMBED_MODEL = os.environ.get("EMBED_MODEL", "text-embedding-nomic-embed-text-v1.5")


def make_llm_func(model):
    """Create an LLM function bound to a specific model."""

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
            "model": model,
            "messages": messages,
            "max_tokens": -1,
            "temperature": 0.0,
            "stop": ["<|COMPLETE|>"],
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
        content = THINK_RE.sub("", content).strip()
        finish = data["choices"][0].get("finish_reason", "unknown")
        usage = data.get("usage", {})
        reasoning_len = len(data["choices"][0]["message"].get("reasoning_content", ""))
        print(f"  [llm] input={len(prompt)}chars output={len(content)}chars finish={finish} reasoning={reasoning_len}chars usage={usage}", file=sys.stderr, flush=True)
        if not content:
            raise ValueError("LLM returned empty content")
        return content

    return llm_func


_embed_client = AsyncOpenAI(base_url=f"{LM_STUDIO_BASE}/v1", api_key="lm-studio")


async def embed_func(texts: list[str], **kwargs) -> np.ndarray:
    """Embed texts via LM Studio without sending the dimensions parameter."""
    resp = await _embed_client.embeddings.create(model=EMBED_MODEL, input=texts)
    return np.array([d.embedding for d in resp.data])


async def create_rag(model=INDEX_MODEL):
    rag = LightRAG(
        working_dir=INDEX_DIR,
        llm_model_func=make_llm_func(model),
        llm_model_max_async=8,  # LM Studio supports continuous batching (default max: 4, but configurable)
        entity_extract_max_gleaning=0,  # skip gleaning pass — initial extraction is sufficient
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
