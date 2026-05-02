// k6/pessimistic.js — SELECT FOR UPDATE (pessimistic locking)
//
// Correct — no money vanishes. Expect 503s under peak load (lock queue exhausts
// the 2s context timeout). That is the intended behaviour.
//
// Usage:
//   curl -X POST http://localhost:8080/reset
//   k6 run k6/pessimistic.js
//   curl http://localhost:8080/balance   # balance will be correct

import http from "k6/http";
import { check } from "k6";

const BASE = "http://localhost:8080";
const TARGET_URL = BASE + "/deduct/pessimistic";

export let options = {
  stages: [
    { duration: "10s", target: 200 },
    { duration: "20s", target: 500 },
    { duration: "20s", target: 800 },
  ],
  thresholds: {
    http_req_failed:   ["rate<0.3"],   // 503s expected under contention
    http_req_duration: ["p(95)<5000"],
  },
};

export default function () {
  const res = http.get(TARGET_URL);
  check(res, {
    "status 200 or 503": (r) => r.status === 200 || r.status === 503,
    "not a 500":         (r) => r.status !== 500,
  });
}

export function handleSummary(data) {
  return {
    stdout: `
════════════════════════════════════════════════════════════
  MODE:   pessimistic (SELECT FOR UPDATE)
  URL:    ${TARGET_URL}
  ✅  Data integrity guaranteed.
  ⚠️  Expect 503s under high concurrency — that is correct behaviour.

  After the run:
    curl http://localhost:8080/balance
    ↳ balance = 1,000,000 − (successful_deductions × 10)
════════════════════════════════════════════════════════════
`,
  };
}
