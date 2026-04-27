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

MONTHS = ["January", "February", "March", "April", "May", "June",
         "July", "August", "September", "October", "November", "December"]

SCRIPT_DIR = os.path.dirname(os.path.abspath(__file__))
INDEX_DIR = os.path.join(SCRIPT_DIR, ".rag_index")


def humanize_exif_date(raw):
    """'2024:04:21 16:27:54' → '21 April 2024 at 16:27:54' (or None on parse failure).

    Mirrors cmd/cashier/photo.go formatDate so the human-readable form is
    consistent between the rendered MD and the indexed/verified text.
    """
    if not raw:
        return None
    parts = raw.split()
    date_parts = parts[0].split(":") if parts else []
    if len(date_parts) != 3:
        return None
    try:
        year, month, day = int(date_parts[0]), int(date_parts[1]), int(date_parts[2])
    except ValueError:
        return None
    if not (1 <= month <= 12):
        return None
    base = f"{day} {MONTHS[month - 1]} {year}"
    if len(parts) > 1:
        return f"{base} at {parts[1]}"
    return base


def build_document(data):
    """Build a single text document from a photo description JSON.

    Used by both the indexer (to create chunks for embedding/extraction) and
    the search verifier (to feed the LLM yes/no relevance check). Keeping a
    single source means whatever was indexed is exactly what gets verified.
    """
    parts = []

    # Photo identity
    parts.append(f"Photo: {data['name']}")
    parts.append(f"File: {data['file']}")

    meta = data.get("metadata", {})

    # Camera
    if meta.get("make") or meta.get("model"):
        camera = f"{meta.get('make', '')} {meta.get('model', '')}".strip()
        parts.append(f"Camera: {camera}")

    # Lens (lens_model preferred, lens_info as fallback)
    if meta.get("lens_model"):
        parts.append(f"Lens: {meta['lens_model']}")
    elif meta.get("lens_info"):
        parts.append(f"Lens: {meta['lens_info']}")

    # Date — keep raw EXIF and add human-readable form for natural-language queries
    if meta.get("date_time_original"):
        raw = meta["date_time_original"]
        parts.append(f"Date: {raw}")
        human = humanize_exif_date(raw)
        if human:
            parts.append(f"Captured on {human}")

    # Settings — comma-joined sentence covering aperture / shutter / ISO /
    # focal length / 35mm equivalent / exposure mode / white balance.
    settings = []
    if meta.get("focal_length"):
        settings.append(meta["focal_length"])
    if meta.get("focal_length_in_35mm"):
        settings.append(f"{meta['focal_length_in_35mm']} (35mm equivalent)")
    if meta.get("f_number"):
        settings.append(f"f/{meta['f_number']}")
    if meta.get("exposure_time"):
        settings.append(f"{meta['exposure_time']}s")
    if meta.get("iso"):
        settings.append(f"ISO {meta['iso']}")
    if meta.get("exposure_mode"):
        settings.append(f"{meta['exposure_mode']} exposure")
    if meta.get("white_balance"):
        settings.append(f"{meta['white_balance']} white balance")
    if settings:
        parts.append(f"Settings: {', '.join(settings)}")

    # Flash
    if meta.get("flash"):
        parts.append(f"Flash: {meta['flash']}")

    # Processing software (e.g. DxO PureRAW, Lightroom)
    if meta.get("software"):
        parts.append(f"Software: {meta['software']}")

    # Photographer attribution
    if meta.get("artist"):
        parts.append(f"Photographer: {meta['artist']}")

    # Visual description (the main content for graph extraction and embedding)
    if data.get("description"):
        parts.append("")
        parts.append(data["description"])

    return "\n".join(parts)

LM_STUDIO_BASE = os.environ.get("LM_STUDIO_BASE", "http://localhost:1234")
INDEX_MODEL = os.environ.get("INDEX_MODEL", "mistralai/ministral-3-3b")
SEARCH_MODEL = os.environ.get("SEARCH_MODEL", "mistralai/ministral-3-3b")
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
        }

        async with httpx.AsyncClient(timeout=600) as client:
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


async def create_rag(model=INDEX_MODEL, cosine_threshold=None):
    extra = {}
    if cosine_threshold is not None:
        extra["cosine_better_than_threshold"] = cosine_threshold
    rag = LightRAG(
        working_dir=INDEX_DIR,
        llm_model_func=make_llm_func(model),
        **extra,
        llm_model_max_async=8,  # LM Studio supports continuous batching (default max: 4, but configurable)
        max_parallel_insert=8,  # process 8 documents through the pipeline concurrently (default: 2)
        default_llm_timeout=600,  # worker timeout = 2x this; 8 concurrent requests need more headroom
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
