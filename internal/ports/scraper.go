package ports

import (
	"context"

	"github.com/makeforme/leadscraper/internal/domain"
)

type ScrapeParams struct {
	Category string
	Location string
	Limit    int
}

type BusinessResult struct {
	Business domain.Business
	Err      error
}

type SourceAdapter interface {
	Name() string
	Scrape(ctx context.Context, params ScrapeParams, results chan<- BusinessResult) error
}
