package postgres

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// ErrNotFound lets callers distinguish "no such row" from a real failure
// without leaking pgx into the rest of the codebase.
var ErrNotFound = errors.New("not found")

func mapNoRows(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// nullStr converts an empty string to a SQL NULL. The schema makes these
// columns nullable, and storing "" would defeat every `IS NOT NULL` filter
// and the partial unique dedup index.
func nullStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func f64(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}

func i32(p *int32) int {
	if p == nil {
		return 0
	}
	return int(*p)
}

// jsonOrEmpty guarantees a valid JSON document for a JSONB column; pgx rejects
// a zero-length RawMessage.
func jsonOrEmpty(raw json.RawMessage) []byte {
	if len(raw) == 0 {
		return []byte(`{}`)
	}
	return raw
}

// nullTime maps the zero time to SQL NULL so the column default can apply.
func nullTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}
