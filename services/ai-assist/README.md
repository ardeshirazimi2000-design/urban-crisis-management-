# ai-assist

Non-authoritative Persian report scorer (ADR-004). `python -m app.worker` consumes `report.created.v1` and produces
`report.scored.v1` (deterministic event ids → downstream de-duplication). `uvicorn app.main:app` exposes `POST /score`.
`type_confidence` is the share of matched evidence, not the probability that a report is true.

```bash
python -m venv .venv && .venv/bin/pip install -r requirements-dev.txt && .venv/bin/python -m pytest -q
```
