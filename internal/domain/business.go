package domain

import (
	"encoding/json"
	"time"
)

type Business struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Address     string          `json:"address,omitempty"`
	Phone       string          `json:"phone,omitempty"`
	Rating      float64         `json:"rating,omitempty"`
	Website     string          `json:"website,omitempty"`
	Category    string          `json:"category,omitempty"`
	Source      string          `json:"source"`
	SourceID    string          `json:"source_id,omitempty"`
	SourceKey   string          `json:"source_key,omitempty"`
	Coordinates *Coordinates    `json:"coordinates,omitempty"`
	Metadata    json.RawMessage `json:"metadata,omitempty"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
}

type Coordinates struct {
	Lat float64 `json:"lat"`
	Lng float64 `json:"lng"`
}

type BusinessFilter struct {
	Category   string
	Source     string
	Location   string
	HasWebsite *bool
	Search     string
	Page       int
	Limit      int
	SortBy     string
	SortOrder  string
}
