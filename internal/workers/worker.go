package workers

import (
	"context"
	"log/slog"
	"math/rand"
	"time"

	"github.com/makeforme/leadscraper/internal/queue"
)

type Worker struct {
	queue queue.Queue
	log   *slog.Logger
}

func NewWorker(q queue.Queue, log *slog.Logger) *Worker {
	return &Worker{queue: q, log: log}
}

func (w *Worker) Start(ctx context.Context) error {
	w.log.Info("worker started, waiting for jobs")

	for {
		select {
		case <-ctx.Done():
			w.log.Info("worker stopped")
			return nil
		default:
			w.processNext(ctx)
		}
	}
}

func (w *Worker) processNext(ctx context.Context) {
	job, err := w.queue.Dequeue(ctx)
	if err != nil {
		w.log.Error("dequeue failed", "error", err)
		time.Sleep(5 * time.Second)
		return
	}

	w.log.Info("processing job", "type", job.Type, "id", job.ID)

	if err := w.execute(ctx, job); err != nil {
		w.log.Error("job failed", "type", job.Type, "id", job.ID, "error", err)
		if reqErr := w.queue.Requeue(ctx, job); reqErr != nil {
			w.log.Error("requeue failed", "id", job.ID, "error", reqErr)
		}
		return
	}

	w.log.Info("job completed", "type", job.Type, "id", job.ID)
	sleepWithJitter()
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
	default:
		w.log.Warn("unknown job type", "type", job.Type)
		return nil
	}
}

func (w *Worker) collectBusiness(ctx context.Context, job *queue.Job) error {
	return nil
}

func (w *Worker) crawlWebsite(ctx context.Context, job *queue.Job) error {
	return nil
}

func (w *Worker) extractContacts(ctx context.Context, job *queue.Job) error {
	return nil
}

func (w *Worker) extractTechnology(ctx context.Context, job *queue.Job) error {
	return nil
}

func (w *Worker) findSocials(ctx context.Context, job *queue.Job) error {
	return nil
}

func (w *Worker) ruleScoring(ctx context.Context, job *queue.Job) error {
	return nil
}

func (w *Worker) aiAudit(ctx context.Context, job *queue.Job) error {
	return nil
}

func (w *Worker) genRecommendation(ctx context.Context, job *queue.Job) error {
	return nil
}

func sleepWithJitter() {
	sleepMs := 2000 + rand.Intn(6000)
	time.Sleep(time.Duration(sleepMs) * time.Millisecond)
}
