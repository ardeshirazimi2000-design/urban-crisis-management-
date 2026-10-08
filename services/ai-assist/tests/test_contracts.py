"""Contract test: events produced by this service validate against contracts/events (CI blocks on drift)."""
import asyncio
import json
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path

from jsonschema import Draft202012Validator
from referencing import Registry, Resource

from app.worker import process

EVENTS = Path(__file__).resolve().parents[3] / "contracts" / "events"


def _validator(name: str) -> Draft202012Validator:
    registry = Registry()
    for f in EVENTS.glob("*.json"):
        registry = registry.with_resource(f.name, Resource.from_contents(json.loads(f.read_text())))
    return Draft202012Validator(json.loads((EVENTS / name).read_text()), registry=registry,
                                format_checker=Draft202012Validator.FORMAT_CHECKER)


def _created():
    return {"event_id": "11111111-1111-1111-1111-111111111111", "event_type": "report.created", "schema_version": 1,
            "aggregate": {"type": "report", "id": "22222222-2222-2222-2222-222222222222", "version": 1},
            "occurred_at": "2026-10-08T20:00:00Z", "producer": "core-service", "correlation_id": "c-123456789",
            "payload": {"report_id": "22222222-2222-2222-2222-222222222222", "report_type": "fire", "description": "آتش",
                        "location": {"lat": 35.7, "lng": 51.4, "accuracy_m": 10}, "received_at": "2026-10-08T20:00:00Z"}}


def test_consumed_fixture_matches_contract():
    errors = list(_validator("report.created.v1.json").iter_errors(_created()))
    assert not errors, errors


def test_produced_scored_event_matches_contract():
    out = asyncio.run(process(_created(), ThreadPoolExecutor(max_workers=1)))
    errors = list(_validator("report.scored.v1.json").iter_errors(out))
    assert not errors, [e.message for e in errors]


def test_contract_rejects_bad_event():
    bad = asyncio.run(process(_created(), ThreadPoolExecutor(max_workers=1)))
    del bad["payload"]["model_version"]
    assert list(_validator("report.scored.v1.json").iter_errors(bad))
