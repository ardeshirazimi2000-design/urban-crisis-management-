#!/usr/bin/env bash
# Drill (AT-09): restore the latest logical backup into a scratch database and measure RTO/RPO.
# Production uses encrypted, immutable backups with PITR (WAL archiving); this script exercises the same checks.
set -euo pipefail
SRC=${SRC:-postgres://crisis:local-only@localhost:5432/crisis}
SCRATCH_DB=${SCRATCH_DB:-crisis_restore_drill}
ADMIN=${ADMIN:-postgres://crisis:local-only@localhost:5432/postgres}
dump=$(mktemp -d)/crisis.dump

t0=$(date +%s)
last_write=$(psql "$SRC" -Atc "select max(received_at) from reports")
pg_dump --format=custom --no-owner "$SRC" -f "$dump"
psql "$ADMIN" -qc "drop database if exists $SCRATCH_DB" -c "create database $SCRATCH_DB"
pg_restore --no-owner -d "${ADMIN%/*}/$SCRATCH_DB" "$dump"
t1=$(date +%s)
restored_last=$(psql "${ADMIN%/*}/$SCRATCH_DB" -Atc "select max(received_at) from reports")
src_count=$(psql "$SRC" -Atc "select count(*) from reports")
dst_count=$(psql "${ADMIN%/*}/$SCRATCH_DB" -Atc "select count(*) from reports")
chain=$(psql "${ADMIN%/*}/$SCRATCH_DB" -Atc "select count(*) from audit_log")
echo "restore duration (RTO component): $((t1 - t0))s"
echo "latest report source=$last_write restored=$restored_last (RPO = difference at failure time)"
echo "reports source=$src_count restored=$dst_count audit_rows=$chain"
[[ "$src_count" == "$dst_count" ]] && echo "PASS" || { echo "FAIL: row count mismatch"; exit 1; }
