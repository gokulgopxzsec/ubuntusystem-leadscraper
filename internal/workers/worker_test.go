package workers

import (
	"context"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/makeforme/leadscraper/internal/queue"
	"github.com/makeforme/leadscraper/pkg/config"
)

// blockingQueue stands in for Redis. Dequeue honours the timeout and the
// context exactly as BRPop does, which is the behaviour under test.
type blockingQueue struct {
	dequeued     atomic.Int64
	requeued     atomic.Int64
	deadLettered atomic.Int64
	jobs         chan queue.Job
}

func newBlockingQueue(jobs ...queue.Job) *blockingQueue {
	q := &blockingQueue{jobs: make(chan queue.Job, len(jobs)+1)}
	for _, j := range jobs {
		q.jobs <- j
	}
	return q
}

func (q *blockingQueue) Enqueue(_ context.Context, job queue.Job) error {
	select {
	case q.jobs <- job:
	default:
	}
	return nil
}

func (q *blockingQueue) Dequeue(ctx context.Context, timeout time.Duration) (*queue.Job, error) {
	select {
	case job := <-q.jobs:
		q.dequeued.Add(1)
		return &job, nil
	case <-time.After(timeout):
		return nil, queue.ErrEmpty
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (q *blockingQueue) Requeue(_ context.Context, job *queue.Job, _ error) error {
	job.RetryCount++
	if job.RetryCount >= 3 {
		q.deadLettered.Add(1)
		return nil
	}
	q.requeued.Add(1)
	return nil
}

func (q *blockingQueue) Len(context.Context) (int64, error)     { return 0, nil }
func (q *blockingQueue) DeadLen(context.Context) (int64, error) { return 0, nil }
func (q *blockingQueue) Close() error                           { return nil }

func testWorker(t *testing.T, q queue.Queue) *Worker {
	t.Helper()

	return NewWorker(q, &Deps{}, config.WorkerConfig{
		Concurrency:  2,
		JobTimeout:   time.Second,
		MinJobDelay:  0,
		MaxJobDelay:  time.Millisecond,
		ShutdownWait: 2 * time.Second,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// The original worker blocked forever inside BRPop with a zero timeout, so the
// ctx.Done() branch was unreachable and SIGTERM could not stop it.
func TestWorkerStopsWhenContextIsCancelled(t *testing.T) {
	w := testWorker(t, newBlockingQueue())

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() { done <- w.Start(ctx) }()

	// Let it settle into the dequeue loop, then ask it to stop.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Start() returned %v, want a clean stop", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not stop within 5s of cancellation; it is wedged in Dequeue")
	}
}

// An unknown job type used to be retried three times before being dead-lettered.
// It can never become known, so it should retire immediately.
func TestUnknownJobTypeIsDiscardedNotRetried(t *testing.T) {
	q := newBlockingQueue(queue.Job{ID: "1", Type: "not_a_real_job"})
	w := testWorker(t, q)

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	if err := w.Start(ctx); err != nil {
		t.Fatalf("Start() = %v", err)
	}

	if got := q.requeued.Load(); got != 0 {
		t.Errorf("unknown job was requeued %d times, want 0", got)
	}
	if got := q.deadLettered.Load(); got != 0 {
		t.Errorf("unknown job was dead-lettered %d times, want 0", got)
	}
}

// A handler that panics must not take the process down with it.
func TestPanicInHandlerIsContainedAndRequeued(t *testing.T) {
	q := newBlockingQueue()
	w := testWorker(t, q)

	// A rule_scoring job with no BusinessID returns an error rather than
	// panicking, so drive the panic path through a nil dependency instead:
	// Deps is empty, so any handler that touches a repo will nil-panic.
	q.jobs <- queue.Job{ID: "1", Type: queue.JobRuleScoring, BusinessID: "some-id"}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	// The assertion is simply that Start returns rather than the process dying.
	if err := w.Start(ctx); err != nil {
		t.Fatalf("Start() = %v", err)
	}

	if q.requeued.Load()+q.deadLettered.Load() == 0 {
		t.Error("a panicking job should have been requeued or dead-lettered, not silently dropped")
	}
}
