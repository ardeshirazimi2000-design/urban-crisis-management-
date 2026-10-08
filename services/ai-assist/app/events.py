"""Event contract helpers (contracts/events/envelope.v1.json)."""
from __future__ import annotations

import uuid
from datetime import datetime, timezone

PRODUCER = "ai-assist"
# Namespace for deterministic event ids: re-processing the same source event yields the same output id,
# so downstream inbox de-duplication makes redelivery harmless.
_NS = uuid.UUID("6f1c1c3e-3f7e-4b8e-9a51-0d5b8f1c2a10")


def scored_event(source: dict, payload: dict, model_version: str) -> dict:
    report_id = source["payload"]["report_id"]
    return {
        "event_id": str(uuid.uuid5(_NS, f"{source['event_id']}:{model_version}")),
        "event_type": "report.scored",
        "schema_version": 1,
        "aggregate": {"type": "report", "id": report_id},
        "occurred_at": datetime.now(timezone.utc).isoformat(),
        "producer": PRODUCER,
        "correlation_id": source.get("correlation_id", ""),
        "causation_id": source["event_id"],
        "payload": {"report_id": report_id, **payload},
    }


def validate_created(event: dict) -> str | None:
    """Return an error string if the report.created envelope is not usable."""
    if event.get("event_type") != "report.created" or event.get("schema_version") != 1:
        return "unsupported event type/version"
    p = event.get("payload")
    if not isinstance(p, dict) or not p.get("report_id") or not event.get("event_id"):
        return "missing report_id/event_id"
    return None
