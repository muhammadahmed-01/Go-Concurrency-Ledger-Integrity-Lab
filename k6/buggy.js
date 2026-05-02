// k6/buggy.js — Lost Update demo: no concurrency control
//
// Fires 800 VUs against /deduct/buggy.
// Money will silently vanish — zero errors, corrupted balance.
//
// Usage:
//   curl -X POST http://localhost:8080/reset
//   k6 run k6/buggy.js
//   curl http://localhost:8080/balance   # will NOT equal expected value

import http from "k6/http";
import { check } from "k6";

const BASE = "http://localhost:8080";
const TARGET_URL = BASE + "/deduct/buggy";

export let options = {
  stages: [
    { duration: "10s", target: 200 },
    { duration: "20s", target: 500 },
    { duration: "20s", target: 800 },
  ],
  thresholds: {
    http_req_failed:   ["rate<0.1"],
    http_req_duration: ["p(95)<2000"],
  },
};

export default function () {
  const res = http.get(TARGET_URL);
  check(res, {
    "status 200": (r) => r.status === 200,
    "not a 500":  (r) => r.status !== 500,
  });
}

export function handleSummary(data) {
  return {
    stdout: `
════════════════════════════════════════════════════════════
  MODE:   buggy (no locking)
  URL:    ${TARGET_URL}
  ⚠️  This mode WILL corrupt data silently.

  After the run:
    curl http://localhost:8080/balance
    ↳ balance will NOT equal expected value — money vanished!
════════════════════════════════════════════════════════════
`,
  };
}
