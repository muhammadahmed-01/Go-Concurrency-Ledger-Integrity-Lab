# Go Concurrency & Ledger Integrity Lab

> **A hands-on experiment demonstrating the Lost Update Problem and three strategies to fix it.**
> Built with Go · PostgreSQL · k6 · Prometheus

📖 **Full war story and deep-dive analysis → [CONCURRENCY_ANALYSIS.md](./CONCURRENCY_ANALYSIS.md)**

---

## The Problem in One Sentence

Two goroutines read the same balance, both deduct $10, both write back — and only one deduction lands. **Money vanishes with a 0% error rate.**

```
Goroutine A:  READ  balance → 1,000,000
Goroutine B:  READ  balance → 1,000,000   ← stale read before A writes

Goroutine A:  WRITE balance = 999,990
Goroutine B:  WRITE balance = 999,990     ← overwrites A. One deduction lost.

Final: 999,990  (should be 999,980)
```

---

## Benchmark Results

> 800 VUs ramped over 50s · starting balance **$1,000,000** · Intel i5-12500H · 16GB RAM

| Metric | Buggy | Optimistic | Pessimistic |
|--------|-------|------------|-------------|
| **Completed Iterations** | 160,207 | 21,798 | 51,914 |
| **Effective Deductions** | **964** ❌ | 12,768 ✅ | 51,293 ✅ |
| **Final Balance** | 990,360 ❌ | 872,320 ✅ | 487,070 ✅ |
| **Money Lost** | **$9,640 vanished** | $0 | $0 |
| **p95 Latency** | ~600ms | ~4,250ms 🔴 | ~2,200ms 🔴 |
| **Error Rate** | **0%** ✅ | ~5–8% (409) | ~15–28% (503) |
| **k6 SLA Passed** | ✅ | ❌ | ✅ |
| **Data Integrity** | ❌ CORRUPTED | ✅ Correct | ✅ Correct |

**Key insight:** Buggy mode passes all SLA checks while silently corrupting data. This is why data integrity bugs are more dangerous than crashes.

---

## Repo Structure

```
/
├── README.md                ← this file
├── CONCURRENCY_ANALYSIS.md  ← full deep-dive: theory, findings, architecture
├── main.go                  ← Go HTTP server (buggy / pessimistic / optimistic endpoints)
├── Dockerfile               ← multi-stage build
├── docker-compose.yml       ← one-command reproducible environment
├── go.mod / go.sum
└── k6/
    ├── buggy.js             ← no locking (money vanishes)
    ├── pessimistic.js       ← SELECT FOR UPDATE
    └── optimistic.js        ← version-column CAS + retry
```

---

## How to Run

### Option A — Docker (recommended, zero setup)

```bash
# 1. Start Postgres + Go server
docker compose up --build

# 2. In another terminal, reset the balance and fire a load test
curl -X POST http://localhost:8080/reset
k6 run k6/buggy.js          # watch money vanish
k6 run k6/pessimistic.js    # correct, serialized
k6 run k6/optimistic.js     # correct, retry-based

# 3. After each run, check the actual balance
curl http://localhost:8080/balance

# 4. Tear down
docker compose down -v
```

### Option B — Local (Postgres already running)

```bash
# Prerequisites: Go 1.22+, PostgreSQL on :5432 (db=testdb, user/pass=postgres), k6

# Start the server
go run main.go

# Reset + run experiments
curl -X POST http://localhost:8080/reset
k6 run k6/buggy.js
curl http://localhost:8080/balance
```

---

## API Endpoints

| Endpoint | Description |
|----------|-------------|
| `POST /reset` | Reset balance to $1,000,000, version to 1 |
| `GET /balance` | Returns `{"balance": N, "version": N}` |
| `GET /deduct/buggy` | **No locking** — demonstrates lost update |
| `GET /deduct/pessimistic` | `SELECT FOR UPDATE` — serialized, correct |
| `GET /deduct/optimistic` | Version CAS + retry — concurrent, correct |
| `GET /metrics` | Prometheus metrics (retries, latency, etc.) |

---

## Three Strategies at a Glance

| Strategy | Mechanism | Correct? | Throughput |
|----------|-----------|----------|------------|
| **Buggy** | Read → sleep → write (no guard) | ❌ | Highest |
| **Pessimistic** | `SELECT ... FOR UPDATE` | ✅ | Lowest |
| **Optimistic** | `UPDATE ... WHERE version = $v` | ✅ | Medium |

📖 **For the full analysis — root cause, findings, retry storm explanation, architectural solutions at scale, and decision framework — see [CONCURRENCY_ANALYSIS.md](./CONCURRENCY_ANALYSIS.md).**

---

## Maps to DDIA

| Concept | Chapter |
|---------|---------|
| Lost Updates | §7.1, §7.4 |
| ACID Isolation | §7.2 |
| Compare-and-Set | §7.4 |
| Throughput vs Consistency | §8–9 |
| Retries & Idempotency | §11.5 |
