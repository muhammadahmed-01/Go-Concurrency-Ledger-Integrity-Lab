package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"time"

	_ "github.com/lib/pq"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// ─── Prometheus metrics ───────────────────────────────────────────────────────

var (
	requestCount = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "http_requests_total",
			Help: "Total number of HTTP requests",
		},
		[]string{"endpoint", "status"},
	)

	requestDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "http_request_duration_seconds",
			Help:    "Request duration",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"endpoint"},
	)

	dbWaitDuration = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "db_pool_wait_seconds",
			Help:    "Time spent waiting for a DB connection from the pool",
			Buckets: []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10},
		},
	)

	optimisticRetries = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "optimistic_lock_retries_total",
			Help: "Number of retries due to optimistic lock conflicts",
		},
	)
)

// ─── Helpers ──────────────────────────────────────────────────────────────────

type statusRecorder struct {
	http.ResponseWriter
	statusCode int
}

// WriteHeader captures the status code before sending it to the client
func (rec *statusRecorder) WriteHeader(code int) {
	rec.statusCode = code
	rec.ResponseWriter.WriteHeader(code)
}

func instrument(endpoint string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		// Initialize with 200, so if the handler finishes without calling WriteHeader, we record success
		rec := &statusRecorder{ResponseWriter: w, statusCode: http.StatusOK}

		defer func() {
			duration := time.Since(start).Seconds()
			requestDuration.WithLabelValues(endpoint).Observe(duration)
			requestCount.WithLabelValues(endpoint, fmt.Sprint(rec.statusCode)).Inc()
		}()

		// Pass the WRAPPER (rec) instead of the original ResponseWriter (w)
		next(rec, r)
	}
}

// ─── Main ─────────────────────────────────────────────────────────────────────

func main() {
	prometheus.MustRegister(requestCount, requestDuration, dbWaitDuration, optimisticRetries)

	db := connectDB()
	defer db.Close()

	initSchema(db)

	http.Handle("/metrics", promhttp.Handler())

	// ── /reset ────────────────────────────────────────────────────────────────
	http.HandleFunc("/reset", instrument("/reset", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()

		_, err := db.ExecContext(ctx, `UPDATE accounts SET balance = 1000000, version = 1 WHERE id = 1`)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprintf(w, "reset failed: %v", err)
			return
		}

		var bal int
		db.QueryRowContext(ctx, `SELECT balance FROM accounts WHERE id = 1`).Scan(&bal)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `{"balance":%d,"message":"account reset to 1000000"}`, bal)
	}))

	// ── /balance ──────────────────────────────────────────────────────────────
	http.HandleFunc("/balance", instrument("/balance", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()

		var bal, ver int
		err := db.QueryRowContext(ctx, `SELECT balance, version FROM accounts WHERE id = 1`).Scan(&bal, &ver)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `{"balance":%d,"version":%d}`, bal, ver)
	}))

	// ── /deduct/buggy ─────────────────────────────────────────────────────────
	http.HandleFunc("/deduct/buggy", instrument("/deduct/buggy", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()

		var balance int
		err := db.QueryRowContext(ctx, `SELECT balance FROM accounts WHERE id = 1`).Scan(&balance)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		if balance < 10 {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, `{"error":"insufficient funds"}`)
			return
		}

		time.Sleep(1 * time.Millisecond)

		_, err = db.ExecContext(ctx, `UPDATE accounts SET balance = $1 WHERE id = 1`, balance-10)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `{"deducted":10,"new_balance":%d}`, balance-10)
	}))

	// ── /deduct/pessimistic ───────────────────────────────────────────────────
	http.HandleFunc("/deduct/pessimistic", instrument("/deduct/pessimistic", func(w http.ResponseWriter, r *http.Request) {
		// War Story Change: 2s timeout
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()

		tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		defer tx.Rollback()

		var balance int
		err = tx.QueryRowContext(ctx, `SELECT balance FROM accounts WHERE id = 1 FOR UPDATE`).Scan(&balance)
		if err != nil {
			// This captures the timeout/contention error
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}

		if balance < 10 {
			tx.Rollback()
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		_, err = tx.ExecContext(ctx, `UPDATE accounts SET balance = balance - 10 WHERE id = 1`)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		if err = tx.Commit(); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `{"deducted":10,"new_balance":%d}`, balance-10)
	}))

	// ── /deduct/optimistic ────────────────────────────────────────────────────
	http.HandleFunc("/deduct/optimistic", instrument("/deduct/optimistic", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()

		const maxRetries = 50
		for attempt := 0; attempt < maxRetries; attempt++ {
			if attempt > 0 {
				optimisticRetries.Inc() // War Story Change: Measurement
			}

			var balance, version int
			err := db.QueryRowContext(ctx, `SELECT balance, version FROM accounts WHERE id = 1`).Scan(&balance, &version)
			if err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}

			if balance < 10 {
				w.WriteHeader(http.StatusBadRequest)
				return
			}

			result, err := db.ExecContext(ctx,
				`UPDATE accounts SET balance = balance - 10, version = version + 1
				 WHERE id = 1 AND version = $1`, version)
			if err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}

			rowsAffected, _ := result.RowsAffected()
			if rowsAffected == 1 {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				fmt.Fprintf(w, `{"deducted":10,"new_balance":%d,"attempt":%d}`, balance-10, attempt+1)
				return
			}
		}

		w.WriteHeader(http.StatusConflict) // Returns 409
	}))

	log.Println("Server running on :8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}

// ─── DB Boilerplate ──────────────────────────────────────────────────────────

func initSchema(db *sql.DB) {
	db.Exec(`CREATE TABLE IF NOT EXISTS accounts (id INT PRIMARY KEY, balance INT NOT NULL, version INT NOT NULL DEFAULT 1)`)
	db.Exec(`INSERT INTO accounts (id, balance, version) VALUES (1, 1000000, 1) ON CONFLICT (id) DO NOTHING`)
}

func connectDB() *sql.DB {
	db, _ := sql.Open("postgres", "postgres://postgres:postgres@localhost:5432/testdb?sslmode=disable")
	db.SetMaxOpenConns(100)
	return db
}
