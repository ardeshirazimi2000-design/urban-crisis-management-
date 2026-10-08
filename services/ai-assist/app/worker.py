"""Kafka worker: consumes report.created.v1, produces report.scored.v1.

At-least-once: offsets are committed only after the output is produced (or the input dead-lettered).
Scoring runs in a bounded thread pool so a slow model never blocks the event loop.
"""
from __future__ import annotations

import asyncio
import json
import logging
import os
from concurrent.futures import ThreadPoolExecutor

from aiokafka import AIOKafkaConsumer, AIOKafkaProducer

from .events import scored_event, validate_created
from .scorer import MODEL_VERSION, score

log = logging.getLogger("ai-assist.worker")


async def process(event: dict, executor: ThreadPoolExecutor, timeout_s: float = 5.0) -> dict:
    p = event["payload"]
    loop = asyncio.get_running_loop()
    try:
        s = await asyncio.wait_for(
            loop.run_in_executor(executor, score, p.get("description", ""), p.get("report_type"),
                                 int(p.get("media_count") or 0), int(p.get("possible_duplicates") or 0)),
            timeout=timeout_s)
        payload = {**s.as_payload(), "status": "ok"}
    except Exception as exc:  # model failure must never block intake: emit a failed status instead
        log.warning("scoring failed: %s", exc)
        payload = {"model_version": MODEL_VERSION, "status": "failed", "signals": {"error": type(exc).__name__}}
    return scored_event(event, payload, MODEL_VERSION)


async def run() -> None:
    brokers = os.environ.get("KAFKA_BROKERS", "localhost:9092")
    prefix = os.environ.get("KAFKA_TOPIC_PREFIX", "crisis.")
    in_topic, out_topic = f"{prefix}report.created.v1", f"{prefix}report.scored.v1"
    executor = ThreadPoolExecutor(max_workers=int(os.environ.get("AI_MAX_CONCURRENCY", "4")))
    consumer = AIOKafkaConsumer(in_topic, bootstrap_servers=brokers, group_id="ai-assist.scorer",
                                enable_auto_commit=False, auto_offset_reset="earliest")
    producer = AIOKafkaProducer(bootstrap_servers=brokers, acks="all", enable_idempotence=True)
    await consumer.start()
    await producer.start()
    log.info("worker started in=%s out=%s model=%s", in_topic, out_topic, MODEL_VERSION)
    try:
        async for msg in consumer:
            try:
                event = json.loads(msg.value)
                err = validate_created(event)
            except (ValueError, TypeError) as exc:
                event, err = None, f"invalid json: {exc}"
            if err:
                await producer.send_and_wait(in_topic + ".dlq", msg.value, key=msg.key,
                                             headers=[("dlq_error", err.encode()), ("dlq_consumer", b"ai-assist")])
            else:
                out = await process(event, executor)
                await producer.send_and_wait(out_topic, json.dumps(out, ensure_ascii=False).encode(),
                                             key=event["payload"]["report_id"].encode(),
                                             headers=[("event_id", out["event_id"].encode())])
            await consumer.commit()
    finally:
        await consumer.stop()
        await producer.stop()
        executor.shutdown(wait=False)


if __name__ == "__main__":
    logging.basicConfig(level=os.environ.get("LOG_LEVEL", "INFO"), format='{"level":"%(levelname)s","msg":"%(message)s"}')
    asyncio.run(run())
