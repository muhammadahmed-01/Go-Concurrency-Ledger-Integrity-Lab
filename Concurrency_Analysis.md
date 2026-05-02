# 💥 The "Money Vanishing" War Story: Concurrency & Data Integrity

> Maps directly to **DDIA Chapter 7 (Transactions)** and **Chapter 8 (Distributed Systems)**.
>
> **Hardware:** Intel i5-12500H · 16GB RAM · Windows 11 (battery — absolute latencies would be lower on AC/server hardware; relative comparisons hold)
> **Stack:** Go · PostgreSQL · Prometheus · Grafana · k6
> **Load:** 800 VUs ramped over 50s (200 → 500 → 800), starting balance: **$1,000,000**

---

## 1. The Problem

Two goroutines simultaneously deduct $10 from the same account. After both finish, only $10 was deducted — not $20. **Money vanished.**

```
Account balance: 1,000,000

Goroutine A:  READ  balance → 1,000,000
Goroutine B:  READ  balance → 1,000,000   ← same stale value, before A writes

Goroutine A:  WRITE balance = 999,990
Goroutine B:  WRITE balance = 999,990     ← overwrites A. One deduction lost.

Final: 999,990  (should be 999,980)
```

This is the **Lost Update Problem** — one of the most common bugs in concurrent systems, and the most dangerous because it produces **no errors**.

---

## 2. Root Cause

**Read-Modify-Write without synchronization.**

```go
// ❌ BUGGY — classic lost update
balance := SELECT balance FROM accounts WHERE id=1  // read
// another goroutine reads the same value here
UPDATE accounts SET balance = balance-10 WHERE id=1  // overwrite, not decrement
```

The check and write are not atomic. Between reading and writing, another goroutine reads the same stale value. Both write back a balance reflecting only *their* deduction.

Maps to **DDIA §7.1 — Dirty Reads and Lost Updates**.

---

## 3. Three Strategies Tested

### Fix #1 — Pessimistic Locking

**Principle:** Assume conflict will happen. Lock the resource before reading.

```sql
BEGIN;
SELECT balance FROM accounts WHERE id = 1 FOR UPDATE;  -- acquires exclusive row lock
-- other goroutines trying FOR UPDATE BLOCK here
UPDATE accounts SET balance = balance - 10 WHERE id = 1;
COMMIT;  -- lock released
```

`SELECT FOR UPDATE` acquires an exclusive row-level lock. Any other transaction attempting to read or write the same row blocks until the first commits or rolls back. Guarantees serialization — no two goroutines inside the critical section simultaneously.

| ✅ Pros | ❌ Cons |
|---|---|
| Correctness guaranteed | Serializes all deductions → lower throughput |
| Simple mental model | Lock contention grows with concurrency |
| Database enforces it | Deadlock risk with multiple rows |

---

### Fix #2 — Optimistic Concurrency Control (OCC)

**Principle:** Assume conflict is *unlikely*. Don't lock — detect conflicts on write and retry.

```sql
-- Read without any lock
SELECT balance, version FROM accounts WHERE id = 1;
-- balance=999,990, version=42

-- Write only if version hasn't changed (Compare-And-Swap)
UPDATE accounts
   SET balance = balance - 10, version = version + 1
 WHERE id = 1 AND version = 42;
-- If another goroutine already incremented version → rows_affected = 0 → RETRY
```

The `WHERE version = $current_version` guard is a CAS operation. If `rows_affected == 0`, a conflict occurred — retry from the read. The database never blocks; goroutines spin instead of waiting.

| ✅ Pros | ❌ Cons |
|---|---|
| Higher throughput when contention is low | CPU burns on retries under high contention |
| No deadlock risk | Retry logic adds code complexity |
| Scales horizontally | Fairness not guaranteed — goroutines can starve |

---

## 4. Real Benchmark Results

> **Key insight:** Completed iterations differ significantly per mode — this is a finding, not noise.
> Buggy has no overhead so all requests complete. Pessimistic serializes. Optimistic hits retry limits.

| Metric | Buggy | Optimistic | Pessimistic |
|--------|-------|------------|-------------|
| **Completed Iterations** | 160,207 | 21,798 | 51,914 |
| **Effective Deductions** | **964** ❌ | 12,768 ✅ | 51,293 ✅ |
| **Final Balance** | 990,360 ❌ | 872,320 ✅ | 487,070 ✅ |
| **Money Lost** | **$9,640 vanished** | $0 | $0 |
| **p95 Latency (peak)** | ~600ms | ~4,250ms 🔴 | ~2,200ms 🔴 |
| **Error Rate** | **0%** ✅ | ~5–8% (409/500) | ~15–28% (503) |
| **k6 SLA Thresholds** | ✅ Passed | ❌ Failed | ✅ Passed |
| **DB Version at End** | 1 (never incremented) | 12,769 ✅ | N/A |
| **Data Integrity** | ❌ CORRUPTED | ✅ Correct | ✅ Correct |

---

## 5. Detailed Findings

### 🚨 Finding 1 — The Invisible Failure (Buggy Mode)

> **The most dangerous failure mode in production.**

- ✅ HTTP 200 on every request, 0% error rate, lowest latency, highest throughput
- ❌ **Only 964 effective deductions out of 160,207 attempts. $9,640 vanished silently.**
- Version column stayed at `1` — every goroutine overwrote the same cycle

**⚡ The Key Insight:** Your monitoring shows all green while silently destroying data. This is why data integrity bugs are more dangerous than crashes. **Crashes are obvious. Silent corruption is not.**

---

### 🚨 Finding 2 — The Retry Storm (Optimistic Mode)

- Only **21,798 iterations completed** — 7× less useful work than buggy mode
- p95 climbed to **~4,250ms** — highest of all three
- `version=12,769` matched `12,768 effective deductions` exactly — correctness maintained, at enormous cost
- Postgres hit **49.6% CPU** just managing retry overhead
- System continued processing in-flight retries **after k6 stopped** — retry storms have a latency tail that outlasts the load

**The version column is the smoking gun:** proves OCC was always correct — but correctness required ~580,000 retries.

---

### 🚨 Finding 3 — The Availability Cliff (Pessimistic Mode)

- **~155 failed requests/second** at peak (503)
- p95 reached **~2,200ms**
- Only **51,914 iterations** — 3× fewer than buggy, but nearly all correct
- Without the 2s context timeout, goroutines would pile up until OOM. **503 is the right production behavior.**

---

## 6. Broader Concepts This Experiment Demonstrates

### ACID Guarantees
The buggy endpoint violates **Isolation** — two transactions observe each other's in-progress state. Pessimistic locking restores it. Maps to **DDIA §7.2**.

### Isolation Levels
- **Read Committed** (PostgreSQL default): prevents dirty reads, but **not** lost updates
- **Repeatable Read / Serializable**: prevents lost updates, higher overhead
- `SELECT FOR UPDATE` at Read Committed gives Serializable-like safety for specific rows without changing the whole transaction's isolation level — surgical and efficient

### Lost Updates — Six Prevention Strategies (DDIA §7.4)
1. Atomic writes (`UPDATE ... SET balance = balance - 10` — safe if no read in between)
2. Explicit locking (`SELECT FOR UPDATE`) — what we tested
3. Compare-and-set (OCC version column) — what we tested
4. Conflict detection (database detects and aborts)
5. Application-level locking (Redis distributed lock)
6. Serializable isolation (nuclear option — correct but expensive)

### Throughput vs Consistency
- Buggy: max throughput, zero consistency
- Pessimistic: min throughput, max consistency
- Optimistic: good throughput *when contention is low*, catastrophic under heavy conflict

This is the fundamental tension in distributed systems — the tradeoff DDIA explores across Chapters 7–9.

### Retries and Idempotency
OCC *requires* retries. If your operation is not idempotent, retrying causes double-charges, double-sends, double-emails. **Always pair retry logic with idempotency keys.** Maps to **DDIA §11.5**.

### What This Looks Like at Stripe/Netflix Scale
At millions of requests/second on the same row, even OCC breaks down. Escalation path:
- **Shard the account** — split one row into N shards, sum on read (reduces hot-row contention by N×)
- **Event sourcing** — append-only log, no in-place updates, replay to get balance
- **CRDT-based balance** — conflict-free replicated data types, mathematically merge concurrent updates without coordination

---

## 7. The Contention Spectrum

```
                    SINGLE HOT ROW (extreme contention)
                               │
    Buggy ─────────────────────┼────────── Pessimistic
   (fast, silent               │           (correct, serialized,
    corruption)                │            503s under load)
                               │
                          Optimistic
                       (correct but retry storm
                        at high contention,
                        excellent at low contention)
                               │
                    MANY INDEPENDENT ROWS (low contention)
                       (OCC wins — conflicts rare,
                        no blocking, horizontally scalable)
```

---

## 8. Real-World Architectural Solutions at Scale

### Option A — LMAX Sequencer (Right Answer for High-Frequency Deductions)

Remove the database from the hot path entirely. Route all deductions through a single-threaded processor fed by an in-memory ring buffer. DB write is async — the processor never waits for disk.

```
[800 VUs] → [Ring Buffer] → [Single Worker Thread] → [Async DB write]
                                (sequential, zero contention)
```

**Why this beats pessimistic + semaphore:** A semaphore reduces how many goroutines fight over the row — the fight still exists. LMAX removes the fight entirely. Processes at memory speed: no locks, no retries, no conflicts. LMAX-based systems handle millions of ops/sec per core.

**Cons:** High implementation complexity, fully async I/O required, hard to scale horizontally.

**Use when:** Payment processors, matching engines, HFT — anywhere hot row contention is unavoidable and throughput targets can't be met with locking.

---

### Option B — Pessimistic Locking + Go Semaphore (Pragmatic Middle Ground)

Cap concurrent DB writers so excess requests queue cheaply in Go, not expensively in Postgres:

```go
var sem = make(chan struct{}, 25)

func deductHandler(w http.ResponseWriter, r *http.Request) {
    sem <- struct{}{}
    defer func() { <-sem }()
    // Now 25 goroutines compete, not 800
    runPessimisticDeduction(r.Context(), db)
}
```

Still DB-row-bound, but eliminates the goroutine flood causing 503 storms. Right choice when simplicity matters more than maximum throughput.

---

### Option C — Sharding, Eventual Consistency, Event Sourcing

| Approach | Use When |
|----------|----------|
| **Sharding** | Many independent accounts, low per-account contention |
| **Async Queue (SQS/Kafka)** | Can tolerate stale reads, non-critical aggregates |
| **Event Sourcing** | Full audit trail required, append-only semantics |

---

## 9. Decision Framework

```
Is contention on a single hot row unavoidable?
├── No (many independent rows) → OCC (Optimistic Locking)
└── Yes
    ├── Maximum throughput required → LMAX Sequencer
    └── Simplicity > max throughput
        ├── Low-medium traffic → Pessimistic + Semaphore
        └── Can tolerate stale reads → Eventual Consistency
```
