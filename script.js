// money_vanishing.js — k6 load test for the Lost Update experiment
//
// ⚠️  Windows / Git Bash users: Git Bash expands paths starting with `/`
//     into Windows paths, breaking env vars like ENDPOINT=/deduct/buggy.
//     Use MODE instead — it's just a plain word with no slashes.
//
// Usage (works on Windows CMD, PowerShell, Git Bash, and Linux/Mac):
//   k6 run -e MODE=buggy        script.js   # no locking  (default)
//   k6 run -e MODE=pessimistic  script.js   # SELECT FOR UPDATE
//   k6 run -e MODE=optimistic   script.js   # version CAS
//
// Before each run, reset the balance:
//   curl -X POST http://localhost:8080/reset
//   curl http://localhost:8080/balance      # should show 100000
//
// After the run, check the actual balance:
//   curl http://localhost:8080/balance
//   With no locking the balance will NOT be 90000 — money vanished!

import http from "k6/http";
import { check } from "k6";

// ── Config ────────────────────────────────────────────────────────────────────

const BASE    = "http://localhost:8080";
const MODE    = (__ENV.MODE || "buggy").toLowerCase();

// Map plain mode names → URL paths (no leading slash issues)
const PATHS = {
  buggy:       BASE + "/deduct/buggy",
  pessimistic: BASE + "/deduct/pessimistic",
  optimistic:  BASE + "/deduct/optimistic",
};

const TARGET_URL = PATHS[MODE] || PATHS["buggy"];

const TOTAL_VUS      = 1000;
const DEDUCT_AMOUNT  = 10;
const START_BALANCE  = 100000;
const EXPECTED_FINAL = START_BALANCE - TOTAL_VUS * DEDUCT_AMOUNT; // 90000

// ── Options ───────────────────────────────────────────────────────────────────

export let options = {
  stages: [
    { duration: "10s", target: 200 },
    { duration: "20s", target: 500 },
    { duration: "20s", target: 800 },
  ],
  thresholds: {
    http_req_failed: ["rate<0.1"],
    http_req_duration: ["p(95)<2000"],
  },
};

// ── Virtual User ──────────────────────────────────────────────────────────────

export default function () {
  const res = http.get(TARGET_URL);

  check(res, {
    "status 200 or 409": (r) => r.status === 200 || r.status === 409,
    "not a 500":         (r) => r.status !== 500,
  });
}

// ── Post-run summary ──────────────────────────────────────────────────────────

export function handleSummary(data) {
  return {
    stdout: `
════════════════════════════════════════════════════════════
  MODE:     ${MODE}
  URL:      ${TARGET_URL}
  VUs:      ${TOTAL_VUS}
  Expected final balance (if correct): ${EXPECTED_FINAL}

  ➜  curl http://localhost:8080/balance
  ➜  If balance ≠ ${EXPECTED_FINAL}, money vanished (lost update!)
════════════════════════════════════════════════════════════
`,
  };
}

