package domain

import "time"

type SocialProfile struct {
	ID         string    `json:"id"`
	BusinessID string    `json:"business_id"`
	Platform   string    `json:"platform"`
	URL        string    `json:"url"`
	Followers  int       `json:"followers,omitempty"`
	Verified   bool      `json:"verified,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}
