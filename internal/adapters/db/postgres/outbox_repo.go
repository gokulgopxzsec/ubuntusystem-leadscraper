package postgres

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// OutboxRepo is the transactional outbox.
//
// Redis cannot participate in a Postgres transaction, so "commit the businesses,
// then enqueue their jobs" leaves a window: a crash between the two leaves the
// businesses stored and no job to ever crawl or score them. They are not failed,
// they are invisible.
//
// Jobs go into this table in the same transaction as the data they refer to, and
// a relay moves them to Redis afterwards. A crash can now only ever deliver a job
// twice, never zero times — which is why the handlers must be idempotent.
type OutboxRepo struct {
	pool *pgxpool.Pool
}

func NewOutboxRepo(pool *pgxpool.Pool) *OutboxRepo {
	return &OutboxRepo{pool: pool}
}

// OutboxEntry is one pending job.
type OutboxEntry struct {
	ID      int64
	Payload json.RawMessage
}

// EnqueueTx writes jobs inside the caller's transaction. This is the whole point
// of the type: it must never open a transaction of its own.
func EnqueueTx(ctx context.Context, tx pgx.Tx, payloads ...json.RawMessage) error {
	for _, p := range payloads {
		if _, err := tx.Exec(ctx,
			`INSERT INTO job_outbox (payload) VALUES ($1)`, []byte(p)); err != nil {
			return fmt.Errorf("write outbox entry: %w", err)
		}
	}
	return nil
}

// Claim locks and returns the next batch of unpublished jobs.
//
// FOR UPDATE SKIP LOCKED is what makes more than one relay safe: a second relay
// steps over the rows this one is holding rather than blocking on them or, worse,
// publishing them a second time.
func (r *OutboxRepo) Claim(ctx context.Context, limit int) (pgx.Tx, []OutboxEntry, error) {
	if limit <= 0 {
		limit = 100
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("begin outbox claim: %w", err)
	}

	rows, err := tx.Query(ctx, `
		SELECT id, payload
		FROM job_outbox
		WHERE published_at IS NULL
		ORDER BY id
		LIMIT $1
		FOR UPDATE SKIP LOCKED`, limit)
	if err != nil {
		tx.Rollback(ctx)
		return nil, nil, fmt.Errorf("claim outbox: %w", err)
	}

	var entries []OutboxEntry
	for rows.Next() {
		var e OutboxEntry
		if err := rows.Scan(&e.ID, &e.Payload); err != nil {
			rows.Close()
			tx.Rollback(ctx)
			return nil, nil, fmt.Errorf("scan outbox entry: %w", err)
		}
		entries = append(entries, e)
	}
	rows.Close()

	if err := rows.Err(); err != nil {
		tx.Rollback(ctx)
		return nil, nil, fmt.Errorf("read outbox: %w", err)
	}

	return tx, entries, nil
}

// MarkPublished is called inside the claim transaction, after the jobs have
// actually reached Redis. Committing the mark and releasing the lock together is
// what makes the hand-off atomic from the database's point of view.
func MarkPublished(ctx context.Context, tx pgx.Tx, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := tx.Exec(ctx,
		`UPDATE job_outbox SET published_at = now() WHERE id = ANY($1)`, ids)
	if err != nil {
		return fmt.Errorf("mark outbox published: %w", err)
	}
	return nil
}

// RecordFailure notes that a batch could not be published. The rows stay
// unpublished and will be retried.
func (r *OutboxRepo) RecordFailure(ctx context.Context, ids []int64, cause error) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := r.pool.Exec(ctx, `
		UPDATE job_outbox
		SET attempts = attempts + 1, last_error = $2
		WHERE id = ANY($1)`, ids, cause.Error())
	if err != nil {
		return fmt.Errorf("record outbox failure: %w", err)
	}
	return nil
}

// PendingCount powers the readiness endpoint: a growing backlog means the relay
// is wedged, and that is otherwise invisible.
func (r *OutboxRepo) PendingCount(ctx context.Context) (int64, error) {
	var n int64
	err := r.pool.QueryRow(ctx,
		`SELECT count(*) FROM job_outbox WHERE published_at IS NULL`).Scan(&n)
	return n, err
}

// Purge drops published rows older than the retention window, so the table does
// not grow without bound.
func (r *OutboxRepo) Purge(ctx context.Context, olderThanHours int) (int64, error) {
	tag, err := r.pool.Exec(ctx, fmt.Sprintf(`
		DELETE FROM job_outbox
		WHERE published_at IS NOT NULL
		  AND published_at < now() - interval '%d hours'`, olderThanHours))
	if err != nil {
		return 0, fmt.Errorf("purge outbox: %w", err)
	}
	return tag.RowsAffected(), nil
}

// Pool exposes the pool so callers can open the transaction that both their data
// writes and their outbox writes share.
func (r *OutboxRepo) Pool() *pgxpool.Pool { return r.pool }
