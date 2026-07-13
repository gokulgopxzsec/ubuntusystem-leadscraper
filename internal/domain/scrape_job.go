package domain

import (
	"encoding/json"
	"time"
)

type ScrapeJob struct {
	ID           string          `json:"id"`
	Source       string          `json:"source"`
	Category     string          `json:"category"`
	Location     string          `json:"location"`
	Status       string          `json:"status"`
	TotalFound   int             `json:"total_found"`
	SuccessCount int             `json:"success_count"`
	FailCount    int             `json:"fail_count"`
	Params       json.RawMessage `json:"params,omitempty"`
	StartedAt    *time.Time      `json:"started_at,omitempty"`
	CompletedAt  *time.Time      `json:"completed_at,omitempty"`
	Error        string          `json:"error,omitempty"`
	CreatedAt    time.Time       `json:"created_at"`
}

type JobFilter struct {
	Source   string
	Category string
	Status   string
	Page     int
	Limit    int
}
