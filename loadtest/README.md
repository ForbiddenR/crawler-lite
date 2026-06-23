# Load testing

Two complementary harnesses, run against a live stack:

| Harness | What it measures | Tool |
|---|---|---|
| `api.js` | HTTP API throughput + latency under concurrent VUs | k6 |
| `queue_burst.sh` | End-to-end pipeline throughput (dispatch → run → terminal) | sh + curl + jq |

`api.js` finds API bottlenecks (DB pool, slow queries, serialization).
`queue_burst.sh` finds dispatch/execution bottlenecks (dispatcher loop,
gRPC handoff, Python spawn cost). Run both; they catch different things.

## Prerequisites

1. Stack up: `make prod-up` (master + ≥1 worker + Postgres/Redis/MinIO).
2. Seeded admin user (see `deploy/RUNBOOK.md` §2).
3. A synced spider that finishes in a few seconds. Use the fixture at
   `fixtures/quick_spider/` — push it to a git repo (or use a `file://`
   URL), create the spider with `entry_module=spider:QuickSpider`, sync
   it, and note its ID. The queue burst needs this ID.

Tools: `k6` (for api.js), `curl` + `jq` + `bc` (for queue_burst.sh).

## api.js (k6 HTTP perf)

```sh
k6 run \
  -e BASE_URL=http://localhost:80 \
  -e LOGIN_EMAIL=admin@example.com \
  -e LOGIN_PASSWORD='your-password' \
  -e SPIDER_ID=1 \
  --summary-export results/api-results.json \
  api.js
```

Stages: ramp 0→10 VUs over 30s, hold 2m, ramp 10→50 VUs over 30s, hold
2m, drain. ~5m total. Thresholds (fail the run if violated):

- `list_duration` p95 < 300ms — the read endpoints (spider/task lists)
  must stay snappy.
- `http_req_failed` < 1% — no 5xx storms.

`create task` is exercised but not thresholded — task creation latency
includes DB writes and is allowed to be higher than reads.

## queue_burst.sh (pipeline throughput)

```sh
BASE_URL=http://localhost:80 \
LOGIN_EMAIL=admin@example.com \
LOGIN_PASSWORD='your-password' \
SPIDER_ID=1 \
N=50 \
./queue_burst.sh
```

Queues `N` tasks against the spider in a tight loop, polls until all are
terminal, and writes `results/queue-burst-<ts>.txt` with:

- `wall_seconds` — start-of-queue → last-terminal
- `throughput_tasks_per_min` — `N * 60 / wall_seconds`
- per-task `p50` / `p95` / `min` / `max` of `finished_at - queued_at`

Interpretation: with 1 worker at `WORKER_CONCURRENCY=4`, expect
throughput ≈ `4 × (60 / per_task_p50)` tasks/min, capped by dispatch
overhead. Scaling workers should scale roughly linearly until the
master's DB pool or gRPC stream becomes the bottleneck.

## Results

Results land in `loadtest/results/`. Don't commit them (`.gitignore`
should exclude, or add `results/` to your gitignore). Copy the headline
numbers into `deploy/RUNBOOK.md` §6 after a clean run so the next
operator has a baseline.
