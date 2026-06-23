#!/bin/sh
# End-to-end pipeline throughput test.
#
# Queues N tasks against an existing spider in a tight loop, then polls
# until all reach a terminal state. Records wall time, success/fail
# counts, and p50/p95 of per-task (finished_at - queued_at). This
# exercises the full loop the k6 HTTP test skips: master dispatch →
# gRPC handoff → worker Python execution → status persistence.
#
# Prereqs:
#   - master + at least one worker running
#   - a seeded admin user
#   - an existing, synced spider whose run completes in a few seconds
#     (see loadtest/fixtures/quick_spider/)
#
# Usage:
#   ./loadtest/queue_burst.sh
#   BASE_URL=http://localhost:80 N=100 SPIDER_ID=3 ./loadtest/queue_burst.sh
#
# Requires: curl, jq.

set -eu

: "${BASE_URL:=http://localhost:80}"
: "${LOGIN_EMAIL:=admin@example.com}"
: "${LOGIN_PASSWORD:?set LOGIN_PASSWORD}"
: "${SPIDER_ID:?set SPIDER_ID}"
: "${N:=50}"
: "${POLL_INTERVAL:=1}"   # seconds between status polls

mkdir -p "$(pwd)/loadtest/results"

echo "==> Logging in"
TOKEN=$(curl -fsS "${BASE_URL}/api/auth/login" \
    -H 'Content-Type: application/json' \
    -d "{\"email\":\"${LOGIN_EMAIL}\",\"password\":\"${LOGIN_PASSWORD}\"}" \
    | jq -r .token)
if [ -z "$TOKEN" ] || [ "$TOKEN" = "null" ]; then
    echo "error: login failed" >&2
    exit 1
fi
AUTH="Authorization: Bearer ${TOKEN}"

echo "==> Queuing ${N} tasks against spider ${SPIDER_ID}"
START=$(date +%s.%N)
TASK_IDS=""
for i in $(seq 1 "$N"); do
    ID=$(curl -fsS "${BASE_URL}/api/tasks" \
        -H 'Content-Type: application/json' \
        -H "$AUTH" \
        -d "{\"spider_id\":${SPIDER_ID},\"args\":{}}" \
        | jq -r .id)
    TASK_IDS="${TASK_IDS} ${ID}"
done
echo "    queued: $(echo $TASK_IDS | wc -w)"

echo "==> Waiting for all tasks to reach a terminal state"
# Terminal statuses: succeeded, failed, cancelled, timeout, captcha_blocked.
while :; do
    PENDING=0
    for ID in $TASK_IDS; do
        STATUS=$(curl -fsS "${BASE_URL}/api/tasks/${ID}" -H "$AUTH" | jq -r .status)
        case "$STATUS" in
            succeeded|failed|cancelled|timeout|captcha_blocked) ;;
            *) PENDING=$((PENDING + 1)) ;;
        esac
    done
    [ "$PENDING" -eq 0 ] && break
    echo "    pending: ${PENDING}"
    sleep "$POLL_INTERVAL"
done
END=$(date +%s.%N)

echo "==> Computing results"
WALL=$(echo "$END - $START" | bc)
echo "    wall time: ${WALL}s for ${N} tasks"

# Per-task durations: finished_at - queued_at, in seconds.
TMP=$(mktemp)
for ID in $TASK_IDS; do
    curl -fsS "${BASE_URL}/api/tasks/${ID}" -H "$AUTH" \
        | jq -r '[.queued_at, .finished_at] | @tsv' >> "$TMP"
done

# jq computes p50/p95 from the duration column.
STATS=$(jq -R 'split("\t") | {q: .[0], f: .[1]}
        | select(.f and .f != "null")
        | ((.f | fromdateiso8601) - (.q | fromdateiso8601))' "$TMP" \
    | jq -s '{count: length,
              p50: (sort | .[length/2 | floor]),
              p95: (sort | .[length*0.95 | floor]),
              min: min,
              max: max}')
rm -f "$TMP"

STAMP=$(date -u +%Y%m%dT%H%M%SZ)
OUT="loadtest/results/queue-burst-${STAMP}.txt"
{
    echo "queue_burst results — ${STAMP}"
    echo "base_url: ${BASE_URL}"
    echo "spider_id: ${SPIDER_ID}"
    echo "n_tasks: ${N}"
    echo "wall_seconds: ${WALL}"
    echo "throughput_tasks_per_min: $(echo "scale=2; $N * 60 / $WALL" | bc)"
    echo "per_task_seconds:"
    echo "$STATS" | sed 's/^/  /'
} | tee "$OUT"

echo "==> Written to ${OUT}"
