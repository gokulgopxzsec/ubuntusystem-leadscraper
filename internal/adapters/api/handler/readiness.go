package handler

import (
	"context"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/makeforme/leadscraper/internal/queue"
)

type ReadinessResponse struct {
	Status    string            `json:"status"`
	Version   string            `json:"version"`
	Checks    map[string]string `json:"checks"`
	QueueLen  int64             `json:"queue_depth"`
	DeadLen   int64             `json:"dead_letter_depth"`
	Timestamp string            `json:"timestamp"`
}

// Readiness actually talks to Postgres and Redis. /health only proves the
// process is up, which is not the same as it being able to do any work.
func Readiness(version string, pool *pgxpool.Pool, q queue.Queue) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()

		resp := ReadinessResponse{
			Status:    "ok",
			Version:   version,
			Checks:    map[string]string{},
			Timestamp: time.Now().UTC().Format(time.RFC3339),
		}
		status := http.StatusOK

		if err := pool.Ping(ctx); err != nil {
			resp.Checks["postgres"] = "error: " + err.Error()
			resp.Status = "degraded"
			status = http.StatusServiceUnavailable
		} else {
			resp.Checks["postgres"] = "ok"
		}

		if depth, err := q.Len(ctx); err != nil {
			resp.Checks["redis"] = "error: " + err.Error()
			resp.Status = "degraded"
			status = http.StatusServiceUnavailable
		} else {
			resp.Checks["redis"] = "ok"
			resp.QueueLen = depth

			if dead, err := q.DeadLen(ctx); err == nil {
				resp.DeadLen = dead
			}
		}

		writeJSON(w, status, resp)
	}
}
