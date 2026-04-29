"""
Shared config + Postgres helpers for the photo library.

Used by index_and_vectorize.py and search.py. The pgvector path replaces the
LightRAG layer entirely — no graph store, no entity extraction; just chunk →
embed → INSERT, and SELECT ... ORDER BY embedding <=> $1 on the way out.
"""

import os
import re
import sys

import asyncpg
import numpy as np
from openai import AsyncOpenAI
from pgvector.asyncpg import register_vector


THINK_RE = re.compile(r"<think>.*?</think>", re.DOTALL)

MONTHS = ["January", "February", "March", "April", "May", "June",
         "July", "August", "September", "October", "November", "December"]


# ── Postgres connection ──────────────────────────────────────────────────

LIBRARY_DSN = os.environ.get("LIBRARY_DSN", "postgres:///ragotogar")


async def connect_library(dsn=None):
    """Open an asyncpg connection to the library DB with the pgvector type
    adapter registered. Caller is responsible for closing the conn."""
    conn = await asyncpg.connect(dsn or LIBRARY_DSN)
    await register_vector(conn)
    return conn


async def fetch_photo_dict(conn, name):
    """Return a dict shaped like the legacy describe JSON for a single photo,
    so build_document() can be reused unchanged. Returns None if the photo
    isn't in the DB."""
    row = await conn.fetchrow("""
        SELECT p.name, p.file_path, p.file_basename,
               e.camera_make, e.camera_model, e.lens_model, e.lens_info,
               e.date_taken, e.focal_length_mm, e.focal_length_35mm,
               e.f_number, e.exposure_time_seconds, e.iso,
               e.exposure_compensation, e.exposure_mode, e.metering_mode,
               e.white_balance, e.flash, e.image_width, e.image_height,
               e.gps_latitude, e.gps_longitude, e.artist, e.software,
               d.subject, d.setting, d.light, d.colors, d.composition,
               d.full_description
        FROM photos p
        LEFT JOIN exif e         ON p.id = e.photo_id
        LEFT JOIN descriptions d ON p.id = d.photo_id
        WHERE p.name = $1
    """, name)
    if row is None:
        return None
    meta = {}
    for src, dst in [
        ("camera_make", "make"),
        ("camera_model", "model"),
        ("lens_model", "lens_model"),
        ("lens_info", "lens_info"),
        ("exposure_mode", "exposure_mode"),
        ("metering_mode", "metering_mode"),
        ("white_balance", "white_balance"),
        ("flash", "flash"),
        ("artist", "artist"),
        ("software", "software"),
    ]:
        v = row[src]
        if v:
            meta[dst] = v
    if row["focal_length_mm"] is not None:
        meta["focal_length"] = f"{row['focal_length_mm']} mm"
    if row["focal_length_35mm"] is not None:
        meta["focal_length_in_35mm"] = f"{row['focal_length_35mm']} mm"
    if row["f_number"] is not None:
        meta["f_number"] = row["f_number"]
    if row["exposure_time_seconds"] is not None:
        s = row["exposure_time_seconds"]
        meta["exposure_time"] = f"1/{int(round(1.0/s))}" if 0 < s < 1 else str(s)
    if row["iso"] is not None:
        meta["iso"] = row["iso"]
    if row["exposure_compensation"] is not None:
        meta["exposure_compensation"] = row["exposure_compensation"]
    if row["image_width"] is not None:
        meta["image_width"] = row["image_width"]
    if row["image_height"] is not None:
        meta["image_height"] = row["image_height"]
    if row["gps_latitude"] is not None:
        meta["gps_latitude"] = row["gps_latitude"]
    if row["gps_longitude"] is not None:
        meta["gps_longitude"] = row["gps_longitude"]
    if row["date_taken"]:
        iso = row["date_taken"]
        if "T" in iso:
            d, t = iso.split("T", 1)
            meta["date_time_original"] = d.replace("-", ":") + " " + t
        else:
            meta["date_time_original"] = iso.replace("-", ":")

    fields = {}
    for k in ("subject", "setting", "light", "colors", "composition"):
        if row[k]:
            fields[k] = row[k]

    return {
        "name": row["name"],
        "file": row["file_basename"] or "",
        "metadata": meta,
        "fields": fields,
        "description": row["full_description"] or "",
    }


async def iter_photo_names(conn):
    """Yield every photo name in alphabetical order."""
    rows = await conn.fetch("SELECT name FROM photos ORDER BY name")
    for r in rows:
        yield r["name"]


# ── document building ────────────────────────────────────────────────────

def humanize_exif_date(raw):
    """'2024:04:21 16:27:54' → '21 April 2024 at 16:27:54' (or None on parse failure)."""
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
    """Build a single text document from a photo description dict.

    The same text gets chunked + embedded for retrieval AND fed to the
    verifier, so any field present in the document can match a query."""
    parts = []
    parts.append(f"Photo: {data['name']}")
    parts.append(f"File: {data['file']}")

    meta = data.get("metadata", {})

    if meta.get("make") or meta.get("model"):
        camera = f"{meta.get('make', '')} {meta.get('model', '')}".strip()
        parts.append(f"Camera: {camera}")

    if meta.get("lens_model"):
        parts.append(f"Lens: {meta['lens_model']}")
    elif meta.get("lens_info"):
        parts.append(f"Lens: {meta['lens_info']}")

    if meta.get("date_time_original"):
        raw = meta["date_time_original"]
        parts.append(f"Date: {raw}")
        human = humanize_exif_date(raw)
        if human:
            parts.append(f"Captured on {human}")

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

    if meta.get("flash"):
        parts.append(f"Flash: {meta['flash']}")

    if meta.get("software"):
        parts.append(f"Software: {meta['software']}")

    if meta.get("artist"):
        parts.append(f"Photographer: {meta['artist']}")

    if data.get("description"):
        parts.append("")
        parts.append(data["description"])

    return "\n".join(parts)


# ── chunking ─────────────────────────────────────────────────────────────

# Chunk size in characters (rough proxy for tokens — nomic-embed-text-v1.5
# accepts 8192 tokens, so we have plenty of headroom). Most photo
# descriptions fit in a single chunk; only the rare long-form description
# spills over.
CHUNK_CHARS = 6000
CHUNK_OVERLAP = 400


def chunk_text(text):
    """Split a document into overlapping character windows. Returns list of
    strings. For documents shorter than CHUNK_CHARS, returns a single chunk."""
    if not text:
        return []
    if len(text) <= CHUNK_CHARS:
        return [text]
    chunks = []
    step = CHUNK_CHARS - CHUNK_OVERLAP
    for start in range(0, len(text), step):
        end = start + CHUNK_CHARS
        chunks.append(text[start:end])
        if end >= len(text):
            break
    return chunks


# ── LM Studio: LLM + embedding clients ───────────────────────────────────

LM_STUDIO_BASE = os.environ.get("LM_STUDIO_BASE", "http://localhost:1234")
INDEX_MODEL = os.environ.get("INDEX_MODEL", "mistralai/ministral-3-3b")
SEARCH_MODEL = os.environ.get("SEARCH_MODEL", "mistralai/ministral-3-3b")
EMBED_MODEL = os.environ.get("EMBED_MODEL", "text-embedding-nomic-embed-text-v1.5")
EMBED_DIM = 768


def make_llm_func(model):
    """Create an LLM function bound to a specific model. Used for verify."""

    async def llm_func(prompt, system_prompt=None, history_messages=[], **kwargs):
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
        if not content:
            raise ValueError("LLM returned empty content")
        return content

    return llm_func


_embed_client = AsyncOpenAI(base_url=f"{LM_STUDIO_BASE}/v1", api_key="lm-studio")


async def embed_texts(texts):
    """Embed a list of texts via LM Studio. Returns a list of numpy arrays
    (each 768-dim). Empty input yields an empty list."""
    if not texts:
        return []
    resp = await _embed_client.embeddings.create(model=EMBED_MODEL, input=texts)
    return [np.array(d.embedding, dtype=np.float32) for d in resp.data]
