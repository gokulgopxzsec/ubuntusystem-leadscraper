package domain

import "time"

type Contact struct {
	ID          string    `json:"id"`
	BusinessID  string    `json:"business_id"`
	Email       string    `json:"email,omitempty"`
	Phone       string    `json:"phone,omitempty"`
	WhatsApp    string    `json:"whatsapp,omitempty"`
	ContactType string    `json:"contact_type"`
	Source      string    `json:"source"`
	Confidence  float64   `json:"confidence"`
	CreatedAt   time.Time `json:"created_at"`
}
