// k6/optimistic.js — Version-column CAS (optimistic concurrency control)
//
// Correct — no money vanishes. Expect 409s (conflict → max retries exceeded).
// Watch the version column climb: it equals total successful deductions.
//
// Usage:
//   curl -X POST http://localhost:8080/reset
//   k6 run k6/optimistic.js
//   curl http://localhost:8080/balance   # balance AND version will be consistent

import http from "k6/http";
import { check } from "k6";

const BASE = "http://localhost:8080";
const TARGET_URL = BASE + "/deduct/optimistic";

export let options = {
  stages: [
    { duration: "10s", target: 200 },
    { duration: "20s", target: 500 },
    { duration: "20s", target: 800 },
  ],
  thresholds: {
    http_req_failed:   ["rate<0.15"],  // 409s expected under heavy contention
    http_req_duration: ["p(95)<6000"],
  },
};

export default function () {
  const res = http.get(TARGET_URL);
  check(res, {
    "status 200 or 409": (r) => r.status === 200 || r.status === 409,
    "not a 500":         (r) => r.status !== 500,
  });
}

export function handleSummary(data) {
  return {
    stdout: `
════════════════════════════════════════════════════════════
  MODE:   optimistic (version CAS)
  URL:    ${TARGET_URL}
  ✅  Data integrity guaranteed.
  ⚠️  Expect 409s — retry exhaustion under extreme contention.

  After the run:
    curl http://localhost:8080/balance
    ↳ balance = 1,000,000 − (version − 1) × 10   (version is the proof)

  Check retry storm in Prometheus:
    curl http://localhost:8080/metrics | grep optimistic_lock_retries_total
════════════════════════════════════════════════════════════
`,
  };
}
