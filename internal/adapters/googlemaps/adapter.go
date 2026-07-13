package googlemaps

import (
	"context"

	"github.com/makeforme/leadscraper/internal/ports"
)

type Adapter struct{}

func NewAdapter() *Adapter {
	return &Adapter{}
}

func (a *Adapter) Name() string { return "google_maps" }

func (a *Adapter) Scrape(ctx context.Context, params ports.ScrapeParams, results chan<- ports.BusinessResult) error {
	defer close(results)
	return nil
}
