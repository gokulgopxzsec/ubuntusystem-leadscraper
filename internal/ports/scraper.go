package ports

import (
	"context"

	"github.com/makeforme/leadscraper/internal/domain"
)

type ScrapeParams struct {
	Category string
	Location string
	Limit    int

	// Query overrides the "<category> in <location>" text built by default.
	Query string
	// File is the CSV path, relative to the configured import directory.
	File string
}

// SearchQuery is the text a search-driven source should run.
func (p ScrapeParams) SearchQuery() string {
	if p.Query != "" {
		return p.Query
	}
	if p.Location != "" {
		return p.Category + " in " + p.Location
	}
	return p.Category
}

// BusinessResult carries one scraped business, or the error that stopped it.
// A failure on a single record must not abort the whole run, so errors travel
// on the same channel as results.
type BusinessResult struct {
	Business domain.Business
	Err      error
}

type SourceAdapter interface {
	Name() string
	Scrape(ctx context.Context, params ScrapeParams, results chan<- BusinessResult) error
}
