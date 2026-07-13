package queue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

type JobType string

const (
	JobCollectBusiness   JobType = "collect_business"
	JobWebsiteCrawl      JobType = "website_crawl"
	JobExtractContacts   JobType = "extract_contacts"
	JobExtractTechnology JobType = "extract_technology"
	JobFindSocials       JobType = "find_socials"
	JobRuleScoring       JobType = "rule_scoring"
	JobAIAudit           JobType = "ai_audit"
	JobGenRecommendation JobType = "gen_recommendation"
)

// ErrEmpty is returned by Dequeue when the poll window elapsed with no job
// available. It is a normal condition, not a failure.
var ErrEmpty = errors.New("queue empty")

const defaultMaxRetries = 3

type Job struct {
	ID         string          `json:"id"`
	Type       JobType         `json:"type"`
	BusinessID string          `json:"business_id,omitempty"`
	WebsiteID  string          `json:"website_id,omitempty"`
	URL        string          `json:"url,omitempty"`
	Payload    json.RawMessage `json:"payload,omitempty"`
	RetryCount int             `json:"retry_count"`
	MaxRetries int             `json:"max_retries"`
	LastError  string          `json:"last_error,omitempty"`
	CreatedAt  time.Time       `json:"created_at"`
}

type Queue interface {
	Enqueue(ctx context.Context, job Job) error
	Dequeue(ctx context.Context, timeout time.Duration) (*Job, error)
	Requeue(ctx context.Context, job *Job, cause error) error
	Len(ctx context.Context) (int64, error)
	DeadLen(ctx context.Context) (int64, error)
	Close() error
}

type RedisQueue struct {
	client *redis.Client
	key    string
}

func NewRedisQueue(client *redis.Client, key string) *RedisQueue {
	return &RedisQueue{client: client, key: key}
}

func (q *RedisQueue) deadKey() string { return q.key + ":dead" }

func (q *RedisQueue) Enqueue(ctx context.Context, job Job) error {
	if job.ID == "" {
		job.ID = uuid.NewString()
	}
	if job.CreatedAt.IsZero() {
		job.CreatedAt = time.Now().UTC()
	}
	if job.MaxRetries == 0 {
		job.MaxRetries = defaultMaxRetries
	}
	data, err := json.Marshal(job)
	if err != nil {
		return fmt.Errorf("marshal job: %w", err)
	}
	if err := q.client.LPush(ctx, q.key, data).Err(); err != nil {
		return fmt.Errorf("lpush %s: %w", q.key, err)
	}
	return nil
}

// Dequeue blocks for at most timeout waiting for a job. A finite timeout is
// what lets a worker notice context cancellation and shut down; blocking
// forever (BRPop with 0) would wedge the process on SIGTERM.
func (q *RedisQueue) Dequeue(ctx context.Context, timeout time.Duration) (*Job, error) {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}

	res, err := q.client.BRPop(ctx, timeout, q.key).Result()
	switch {
	case errors.Is(err, redis.Nil):
		return nil, ErrEmpty
	case err != nil:
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("brpop %s: %w", q.key, err)
	}

	// BRPop returns [key, value].
	if len(res) != 2 {
		return nil, fmt.Errorf("brpop %s: malformed reply of length %d", q.key, len(res))
	}

	var job Job
	if err := json.Unmarshal([]byte(res[1]), &job); err != nil {
		// A payload we cannot parse will never become parseable. Retrying it
		// would spin forever, so park it in the dead list and move on.
		q.client.LPush(ctx, q.deadKey(), res[1])
		return nil, fmt.Errorf("unmarshal job: %w", err)
	}
	return &job, nil
}

// Requeue puts a failed job back on the queue, or moves it to the dead list
// once it has exhausted its retries.
func (q *RedisQueue) Requeue(ctx context.Context, job *Job, cause error) error {
	job.RetryCount++
	if cause != nil {
		job.LastError = cause.Error()
	}
	if job.MaxRetries == 0 {
		job.MaxRetries = defaultMaxRetries
	}

	if job.RetryCount >= job.MaxRetries {
		data, err := json.Marshal(job)
		if err != nil {
			return fmt.Errorf("marshal dead job: %w", err)
		}
		if err := q.client.LPush(ctx, q.deadKey(), data).Err(); err != nil {
			return fmt.Errorf("lpush %s: %w", q.deadKey(), err)
		}
		return nil
	}

	return q.Enqueue(ctx, *job)
}

func (q *RedisQueue) Len(ctx context.Context) (int64, error) {
	return q.client.LLen(ctx, q.key).Result()
}

func (q *RedisQueue) DeadLen(ctx context.Context) (int64, error) {
	return q.client.LLen(ctx, q.deadKey()).Result()
}

func (q *RedisQueue) Close() error {
	return q.client.Close()
}
