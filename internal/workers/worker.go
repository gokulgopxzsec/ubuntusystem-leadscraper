package workers

import (
	"context"
	"errors"
	"log/slog"
	"math/rand"
	"sync"
	"time"

	"github.com/makeforme/leadscraper/internal/queue"
	"github.com/makeforme/leadscraper/pkg/config"
)

// Worker pulls jobs off the queue and runs them with bounded concurrency.
type Worker struct {
	queue queue.Queue
	deps  *Deps
	cfg   config.WorkerConfig
	log   *slog.Logger
}

func NewWorker(q queue.Queue, deps *Deps, cfg config.WorkerConfig, log *slog.Logger) *Worker {
	return &Worker{queue: q, deps: deps, cfg: cfg, log: log}
}

// Start blocks until ctx is cancelled, then waits for in-flight jobs to finish
// (up to ShutdownWait) before returning.
func (w *Worker) Start(ctx context.Context) error {
	w.log.Info("worker started",
		"concurrency", w.cfg.Concurrency,
		"job_timeout", w.cfg.JobTimeout)

	// The buffered channel is the concurrency limiter: a slot must be free
	// before we even ask Redis for the next job, so we never dequeue work we
	// have no capacity to run.
	slots := make(chan struct{}, w.cfg.Concurrency)
	var wg sync.WaitGroup

	for {
		select {
		case <-ctx.Done():
			return w.drain(&wg)
		case slots <- struct{}{}:
		}

		// A finite timeout is what makes cancellation observable; blocking
		// forever here is what wedged the old worker on SIGTERM.
		job, err := w.queue.Dequeue(ctx, 5*time.Second)
		if err != nil {
			<-slots

			switch {
			case errors.Is(err, queue.ErrEmpty):
				continue
			case ctx.Err() != nil:
				return w.drain(&wg)
			default:
				w.log.Error("dequeue failed", "error", err)
				// Usually Redis is unreachable. A tight loop would burn a core
				// achieving nothing, so back off before trying again.
				select {
				case <-time.After(2 * time.Second):
				case <-ctx.Done():
					return w.drain(&wg)
				}
				continue
			}
		}

		wg.Add(1)
		go func(job *queue.Job) {
			defer wg.Done()
			defer func() { <-slots }()
			w.run(ctx, job)
		}(job)
	}
}

func (w *Worker) drain(wg *sync.WaitGroup) error {
	w.log.Info("worker draining in-flight jobs", "timeout", w.cfg.ShutdownWait)

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		w.log.Info("worker stopped cleanly")
	case <-time.After(w.cfg.ShutdownWait):
		w.log.Warn("worker shutdown timed out with jobs still running")
	}
	return nil
}

func (w *Worker) run(parent context.Context, job *queue.Job) {
	log := w.log.With("job_id", job.ID, "type", job.Type)

	// The job body is detached from the parent's cancellation but keeps a hard
	// deadline. A job killed mid-write would leave the database inconsistent,
	// so it gets its full budget to finish even once shutdown has begun.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), w.timeout(job.Type))
	defer cancel()

	// A panic in one handler must not take down the worker process.
	defer func() {
		if r := recover(); r != nil {
			log.Error("job panicked", "panic", r)
			if err := w.queue.Requeue(context.WithoutCancel(parent), job, panicErr(r)); err != nil {
				log.Error("requeue after panic failed", "error", err)
			}
		}
	}()

	start := time.Now()
	log.Debug("job started")

	if err := w.execute(ctx, job); err != nil {
		log.Error("job failed", "error", err,
			"retry_count", job.RetryCount, "elapsed", time.Since(start))

		if err := w.queue.Requeue(context.WithoutCancel(parent), job, err); err != nil {
			log.Error("requeue failed", "error", err)
		}
		return
	}

	log.Info("job completed", "elapsed", time.Since(start))
	w.pause(parent)
}

func (w *Worker) execute(ctx context.Context, job *queue.Job) error {
	switch job.Type {
	case queue.JobCollectBusiness:
		return w.collectBusiness(ctx, job)
	case queue.JobWebsiteCrawl:
		return w.crawlWebsite(ctx, job)
	case queue.JobExtractContacts:
		return w.extractContacts(ctx, job)
	case queue.JobExtractTechnology:
		return w.extractTechnology(ctx, job)
	case queue.JobFindSocials:
		return w.findSocials(ctx, job)
	case queue.JobRuleScoring:
		return w.ruleScoring(ctx, job)
	case queue.JobAIAudit:
		return w.aiAudit(ctx, job)
	case queue.JobGenRecommendation:
		return w.genRecommendation(ctx, job)
	case queue.JobEmbedLead:
		return w.embedLead(ctx, job)
	default:
		// An unknown type will never become known. Returning nil retires it
		// rather than cycling it through three pointless retries.
		w.log.Warn("unknown job type, discarding", "type", job.Type, "job_id", job.ID)
		return nil
	}
}

// timeout is per job type. A collection job drives a headless browser over
// Google Maps and routinely runs for many minutes; the ordinary 2m budget would
// kill every Maps scrape before it produced a single row.
func (w *Worker) timeout(t queue.JobType) time.Duration {
	if t == queue.JobCollectBusiness {
		return w.cfg.CollectTimeout
	}
	return w.cfg.JobTimeout
}

// pause spreads out the requests we make to third-party sites and APIs. It is
// politeness, and it is why throughput is measured in leads per minute.
func (w *Worker) pause(ctx context.Context) {
	delay := w.cfg.MinJobDelay
	if spread := w.cfg.MaxJobDelay - w.cfg.MinJobDelay; spread > 0 {
		delay += time.Duration(rand.Int63n(int64(spread)))
	}

	select {
	case <-time.After(delay):
	case <-ctx.Done():
	}
}

func panicErr(r any) error {
	if err, ok := r.(error); ok {
		return err
	}
	return errors.New("panic in job handler")
}
