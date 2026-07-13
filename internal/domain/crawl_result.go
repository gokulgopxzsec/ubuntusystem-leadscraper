package domain

import "time"

type CrawlResult struct {
	ID         string            `json:"id"`
	WebsiteID  string            `json:"website_id"`
	URL        string            `json:"url"`
	StatusCode int               `json:"status_code,omitempty"`
	HTML       string            `json:"html,omitempty"`
	Title      string            `json:"title,omitempty"`
	MetaTags   map[string]string `json:"meta_tags,omitempty"`
	Links      []string          `json:"links,omitempty"`
	Error      string            `json:"error,omitempty"`
	CrawledAt  time.Time         `json:"crawled_at"`
}
