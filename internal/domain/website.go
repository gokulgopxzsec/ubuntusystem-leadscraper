package domain

import "time"

type Website struct {
	ID              string       `json:"id"`
	BusinessID      string       `json:"business_id"`
	URL             string       `json:"url"`
	StatusCode      int          `json:"status_code,omitempty"`
	LoadTimeMs      int          `json:"load_time_ms,omitempty"`
	HasSSL          bool         `json:"has_ssl"`
	HasBooking      bool         `json:"has_booking"`
	IsMobileFriendly bool        `json:"is_mobile_friendly"`
	PagesCrawled    int          `json:"pages_crawled"`
	Title           string       `json:"title,omitempty"`
	MetaDescription string       `json:"meta_description,omitempty"`
	Technologies    []Technology `json:"technologies,omitempty"`
	CrawledAt       *time.Time   `json:"crawled_at,omitempty"`
	CreatedAt       time.Time    `json:"created_at"`
}

type Technology struct {
	Name     string `json:"name"`
	Version  string `json:"version,omitempty"`
	Category string `json:"category"`
}
