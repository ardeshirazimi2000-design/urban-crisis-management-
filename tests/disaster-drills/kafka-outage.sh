#!/usr/bin/env bash
# Drill (AT-03 / runbook 17.1): Kafka unavailable during report intake.
# Expects the local stack (docker compose) on BASE. Steps:
#   1) stop Kafka  2) submit reports -> must still return 202 (DB+outbox durable)
#   3) confirm outbox backlog grows  4) start Kafka  5) backlog drains, nothing lost.
set -euo pipefail
BASE=${BASE:-http://localhost:8080}
COMPOSE=${COMPOSE:-docker compose}
N=${N:-20}

token=$(curl -fsS -X POST "$BASE/api/v1/dev/token" -H 'Content-Type: application/json' \
  -d '{"subject":"drill-citizen","name":"drill","grants":[]}' | python3 -c 'import sys,json;print(json.load(sys.stdin)["access_token"])')
admin=$(curl -fsS -X POST "$BASE/api/v1/dev/token" -H 'Content-Type: application/json' \
  -d '{"subject":"drill-admin","name":"drill","grants":[{"role":"SECURITY_ADMIN"}]}' | python3 -c 'import sys,json;print(json.load(sys.stdin)["access_token"])')
pending() { curl -fsS "$BASE/api/v1/admin/outbox?state=pending" -H "Authorization: Bearer $admin" | python3 -c 'import sys,json;print(json.load(sys.stdin)["pending"])'; }

start=$(date +%s)
echo "[drill] stopping kafka"; $COMPOSE stop kafka >/dev/null
for i in $(seq 1 "$N"); do
  code=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE/api/v1/reports" -H "Authorization: Bearer $token" \
    -H "Idempotency-Key: drill-$start-$i" -H 'Content-Type: application/json' \
    -d '{"type":"fire","description":"drill","location":{"lat":35.7,"lng":51.4,"accuracy_m":10}}')
  [[ "$code" == "202" ]] || { echo "FAIL: intake returned $code while Kafka down"; exit 1; }
done
echo "[drill] $N reports accepted with Kafka down; outbox pending=$(pending)"
echo "[drill] starting kafka"; $COMPOSE start kafka >/dev/null
for _ in $(seq 1 60); do
  p=$(pending); [[ "$p" == "0" ]] && break; sleep 5
done
[[ "$(pending)" == "0" ]] || { echo "FAIL: outbox did not drain"; exit 1; }
echo "[drill] PASS: backlog drained in $(( $(date +%s) - start ))s (record as measured recovery time)"
