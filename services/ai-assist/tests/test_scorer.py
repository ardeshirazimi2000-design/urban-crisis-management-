import asyncio
from concurrent.futures import ThreadPoolExecutor

from fastapi.testclient import TestClient

from app.events import validate_created
from app.main import app
from app.scorer import MODEL_VERSION, normalize, score
from app.worker import process


def test_normalize_arabic_variants():
    assert normalize("كيك  ۱۲") == "کیک 12"


def test_trapped_people_is_urgent():
    s = score("دو نفر زیر آوار گیر افتاده اند، صدای کمک می آید", "building_collapse")
    assert s.predicted_type == "trapped_people"
    assert s.urgency_signal >= 0.9
    assert s.signals["type_disagreement"] is True


def test_fire():
    s = score("دود غلیظ و آتش در طبقه سوم", "fire")
    assert s.predicted_type == "fire" and not s.signals["type_disagreement"]


def test_media_does_not_raise_urgency():
    a = score("ترک در دیوار", "structural_damage", media_count=0)
    b = score("ترک در دیوار", "structural_damage", media_count=5)
    assert a.urgency_signal == b.urgency_signal and a.type_confidence == b.type_confidence


def test_no_keywords():
    s = score("سلام", None)
    assert s.predicted_type is None and s.type_confidence is None and s.signals["low_information"]


def test_http_score():
    c = TestClient(app)
    r = c.post("/score", json={"description": "بوی گاز در ساختمان", "report_type": "gas_leak"})
    assert r.status_code == 200
    body = r.json()
    assert body["predicted_type"] == "gas_leak" and body["authoritative"] is False and body["model_version"] == MODEL_VERSION
    assert c.post("/score", json={"description": "x" * 5000}).status_code == 422


def _created(eid="11111111-1111-1111-1111-111111111111"):
    return {"event_id": eid, "event_type": "report.created", "schema_version": 1, "correlation_id": "c-123456789",
            "payload": {"report_id": "22222222-2222-2222-2222-222222222222", "report_type": "fire", "description": "آتش", "media_count": 0}}


def test_worker_is_deterministic_and_carries_causation():
    ex = ThreadPoolExecutor(max_workers=1)
    a = asyncio.run(process(_created(), ex))
    b = asyncio.run(process(_created(), ex))
    assert a["event_id"] == b["event_id"], "redelivery must yield the same event id (inbox dedupe)"
    assert a["causation_id"] == "11111111-1111-1111-1111-111111111111"
    assert a["correlation_id"] == "c-123456789" and a["payload"]["status"] == "ok"
    assert a["event_type"] == "report.scored" and a["schema_version"] == 1


def test_validate_created():
    assert validate_created(_created()) is None
    bad = _created()
    bad["schema_version"] = 2
    assert validate_created(bad)
