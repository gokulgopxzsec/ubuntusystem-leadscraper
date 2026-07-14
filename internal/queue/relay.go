package queue

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/makeforme/leadscraper/internal/adapters/db/postgres"
)

// Relay moves jobs from the Postgres outbox onto Redis.
//
// The pipeline writes its follow-up jobs into job_outbox inside the same
// transaction as the data they refer to, because Redis cannot join a Postgres
// transaction. This is the other half of that: it drains the outbox and
// publishes.
//
// The hand-off is at-least-once, deliberately. Publishing to Redis and marking
// the row published cannot be one atomic act, and given the choice between
// possibly delivering a job twice and possibly never delivering it, twice is the
// only safe answer — so the job handlers are written to be idempotent.
type Relay struct {
	outbox *postgres.OutboxRepo
	queue  Queue
	log    *slog.Logger

	Interval  time.Duration
	BatchSize int
}

func NewRelay(outbox *postgres.OutboxRepo, q Queue, log *slog.Logger) *Relay {
	return &Relay{
		outbox:    outbox,
		queue:     q,
		log:       log,
		Interval:  time.Second,
		BatchSize: 100,
	}
}

func (r *Relay) Start(ctx context.Context) error {
	r.log.Info("outbox relay started", "interval", r.Interval, "batch", r.BatchSize)

	ticker := time.NewTicker(r.Interval)
	defer ticker.Stop()

	// Published rows are dead weight; sweep them occasionally so the table does
	// not grow forever.
	purge := time.NewTicker(time.Hour)
	defer purge.Stop()

	for {
		select {
		case <-ctx.Done():
			r.log.Info("outbox relay stopped")
			return nil

		case <-ticker.C:
			// Keep draining while there is a full batch waiting: after a big
			// scrape the outbox holds hundreds of jobs, and one batch per tick
			// would trickle them out.
			for {
				n, err := r.drain(ctx)
				if err != nil {
					r.log.Error("outbox drain failed", "error", err)
					break
				}
				if n < r.BatchSize {
					break
				}
			}

		case <-purge.C:
			if n, err := r.outbox.Purge(ctx, 24); err != nil {
				r.log.Warn("outbox purge failed", "error", err)
			} else if n > 0 {
				r.log.Debug("purged published outbox rows", "count", n)
			}
		}
	}
}

// drain publishes one batch and reports how many it moved.
func (r *Relay) drain(ctx context.Context) (int, error) {
	tx, entries, err := r.outbox.Claim(ctx, r.BatchSize)
	if err != nil {
		return 0, err
	}
	// The claim holds row locks until the transaction ends, so it must always end.
	defer tx.Rollback(ctx)

	if len(entries) == 0 {
		return 0, nil
	}

	ids := make([]int64, 0, len(entries))
	published := 0

	for _, e := range entries {
		var job Job
		if err := json.Unmarshal(e.Payload, &job); err != nil {
			// A payload that will not parse now will not parse later either.
			// Marking it published retires it instead of blocking the queue head
			// forever.
			r.log.Error("dropping unparseable outbox entry", "id", e.ID, "error", err)
			ids = append(ids, e.ID)
			continue
		}

		if err := r.queue.Enqueue(ctx, job); err != nil {
			// Redis is down. Leave the rest unpublished and let the next tick
			// retry: the rows are safe in Postgres, which is the whole point.
			r.log.Warn("could not publish outbox entry, will retry", "id", e.ID, "error", err)

			if len(ids) > 0 {
				break
			}
			if err := r.outbox.RecordFailure(ctx, []int64{e.ID}, err); err != nil {
				r.log.Warn("could not record outbox failure", "error", err)
			}
			return 0, nil
		}

		ids = append(ids, e.ID)
		published++
	}

	if err := postgres.MarkPublished(ctx, tx, ids); err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}

	if published > 0 {
		r.log.Debug("published outbox entries", "count", published)
	}
	return len(entries), nil
}
