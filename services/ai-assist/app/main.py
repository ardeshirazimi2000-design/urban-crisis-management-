"""HTTP API of the AI assist service (synchronous scoring for tools and tests).

The primary integration is the Kafka worker (app.worker). This API is non-authoritative and is not on
the report intake path: if it is down, intake and manual review continue (AT-04).
"""
from __future__ import annotations

import asyncio
from concurrent.futures import ThreadPoolExecutor

from fastapi import FastAPI
from pydantic import BaseModel, Field

from .scorer import MODEL_VERSION, TYPE_KEYWORDS, score

app = FastAPI(title="Urban Crisis AI Assist", version=MODEL_VERSION)
_executor = ThreadPoolExecutor(max_workers=4)
_sem = asyncio.Semaphore(16)  # bounded concurrency; excess requests queue instead of overloading


class ScoreRequest(BaseModel):
    description: str = Field(default="", max_length=4000)
    report_type: str | None = Field(default=None, max_length=40)
    media_count: int = Field(default=0, ge=0, le=20)
    possible_duplicates: int = Field(default=0, ge=0)


@app.get("/health/live")
def live() -> dict:
    return {"status": "ok"}


@app.get("/health/ready")
def ready() -> dict:
    return {"status": "ready", "model_version": MODEL_VERSION}


@app.get("/model")
def model() -> dict:
    return {"model_version": MODEL_VERSION, "classes": list(TYPE_KEYWORDS), "authoritative": False,
            "note": "سیگنال کمکی؛ جایگزین بررسی انسانی نیست"}


@app.post("/score")
async def score_endpoint(req: ScoreRequest) -> dict:
    async with _sem:
        loop = asyncio.get_running_loop()
        s = await loop.run_in_executor(_executor, score, req.description, req.report_type, req.media_count,
                                       req.possible_duplicates)
    return {**s.as_payload(), "authoritative": False}
