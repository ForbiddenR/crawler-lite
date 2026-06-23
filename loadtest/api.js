// k6 load test for the crawler-lite master HTTP API.
//
// Exercises the read path (spider/task lists) and the write path
// (create task) at increasing concurrency. The WebSocket log stream
// and the worker execution loop are out of scope here — see
// queue_burst.sh for end-to-end pipeline throughput.
//
// Prereqs:
//   - master + at least one worker running (make prod-up)
//   - a seeded admin user (see deploy/RUNBOOK.md §2)
//   - an existing, synced spider; set SPIDER_ID
//
// Run:
//   k6 run -e BASE_URL=http://localhost:80 \
//          -e LOGIN_EMAIL=admin@example.com \
//          -e LOGIN_PASSWORD='...' \
//          -e SPIDER_ID=1 \
//          --summary-export loadtest/results/api-results.json \
//          loadtest/api.js
//
// Thresholds fail the run (non-zero exit) if p95 list latency > 300ms
// or error rate > 1%, so this is CI-able.

import http from "k6/http";
import { check, group, sleep } from "k6";
import { Trend } from "k6/metrics";

const BASE = __ENV.BASE_URL || "http://localhost:80";
const EMAIL = __ENV.LOGIN_EMAIL || "admin@example.com";
const PASSWORD = __ENV.LOGIN_PASSWORD || "";
const SPIDER_ID = __ENV.SPIDER_ID || "1";

// Per-group latency trend so thresholds can target list endpoints
// specifically rather than the create-task outlier.
const listLatency = new Trend("list_duration", true);

export const options = {
  stages: [
    { duration: "30s", target: 10 }, // ramp to 10 VUs
    { duration: "2m", target: 10 }, // sustain
    { duration: "30s", target: 50 }, // ramp to 50 VUs
    { duration: "2m", target: 50 }, // sustain
    { duration: "30s", target: 0 }, // drain
  ],
  thresholds: {
    // p95 of the dedicated list metric must stay under 300ms.
    list_duration: ["p(95)<300"],
    http_req_failed: ["rate<0.01"],
  },
};

export function setup() {
  // Login once; the token is shared across all VUs. Each VU could login
  // independently, but a single admin token is closer to a real UI
  // session and keeps the test focused on the read/write endpoints.
  const res = http.post(
    `${BASE}/api/auth/login`,
    JSON.stringify({ email: EMAIL, password: PASSWORD }),
    { headers: { "Content-Type": "application/json" } }
  );
  if (res.status !== 200) {
    throw new Error(`login failed: ${res.status} ${res.body}`);
  }
  return { token: res.json("token") };
}

export default function (data) {
  const auth = { headers: { Authorization: `Bearer ${data.token}` } };

  group("list spiders", () => {
    const r = http.get(`${BASE}/api/spiders`, auth);
    listLatency.add(r.timings.duration);
    check(r, { "spiders 200": (x) => x.status === 200 });
  });

  group("list tasks", () => {
    const r = http.get(`${BASE}/api/tasks?limit=20`, auth);
    listLatency.add(r.timings.duration);
    check(r, { "tasks 200": (x) => x.status === 200 });
  });

  group("create task", () => {
    const r = http.post(
      `${BASE}/api/tasks`,
      JSON.stringify({ spider_id: parseInt(SPIDER_ID, 10), args: {} }),
      { ...auth, headers: { ...auth.headers, "Content-Type": "application/json" } }
    );
    check(r, { "create 200|201": (x) => x.status === 200 || x.status === 201 });
  });

  // A short pause keeps the request rate realistic without padding
  // latency numbers with idle time (k6 excludes sleep from timings).
  sleep(0.2);
}
