package csv

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/makeforme/leadscraper/internal/domain"
	"github.com/makeforme/leadscraper/internal/ports"
)

// Adapter imports businesses from a CSV file. Header names are matched
// case-insensitively against several common spellings, because these files
// come from exports nobody controls.
type Adapter struct {
	dir string
}

func NewAdapter(dir string) *Adapter {
	if dir == "" {
		dir = "data"
	}
	return &Adapter{dir: dir}
}

func (a *Adapter) Name() string { return "csv" }

// aliases maps a canonical field to the header spellings seen in the wild.
var aliases = map[string][]string{
	"name":     {"name", "business_name", "business", "title", "company"},
	"address":  {"address", "full_address", "location", "formatted_address", "street"},
	"phone":    {"phone", "phone_number", "mobile", "contact", "telephone", "tel"},
	"website":  {"website", "url", "site", "web", "domain"},
	"category": {"category", "type", "industry", "business_type", "niche"},
	"rating":   {"rating", "stars", "score", "review_rating"},
	"lat":      {"lat", "latitude"},
	"lng":      {"lng", "lon", "long", "longitude"},
	"key":      {"id", "place_id", "source_key", "external_id"},
}

func (a *Adapter) Scrape(ctx context.Context, params ports.ScrapeParams, results chan<- ports.BusinessResult) error {
	defer close(results)

	path, err := a.resolve(params.File)
	if err != nil {
		return err
	}

	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open csv %s: %w", path, err)
	}
	defer f.Close()

	reader := csv.NewReader(f)
	reader.TrimLeadingSpace = true
	// Rows in these exports are routinely ragged. Tolerate that and validate
	// per field instead of failing the whole import.
	reader.FieldsPerRecord = -1

	header, err := reader.Read()
	if err != nil {
		return fmt.Errorf("read csv header from %s: %w", path, err)
	}
	cols := indexColumns(header)

	if _, ok := cols["name"]; !ok {
		return fmt.Errorf("csv %s has no recognisable name column (got %v)", path, header)
	}

	sent := 0
	for row := 2; ; row++ {
		if params.Limit > 0 && sent >= params.Limit {
			return nil
		}

		rec, err := reader.Read()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			// One malformed line should not kill an import of ten thousand.
			if !send(ctx, results, ports.BusinessResult{
				Err: fmt.Errorf("csv %s line %d: %w", filepath.Base(path), row, err),
			}) {
				return ctx.Err()
			}
			continue
		}

		b, err := toBusiness(rec, cols, params.Category)
		if err != nil {
			if !send(ctx, results, ports.BusinessResult{
				Err: fmt.Errorf("csv %s line %d: %w", filepath.Base(path), row, err),
			}) {
				return ctx.Err()
			}
			continue
		}

		if !send(ctx, results, ports.BusinessResult{Business: *b}) {
			return ctx.Err()
		}
		sent++
	}
}

// resolve keeps the import confined to the configured directory. The filename
// arrives over the API, so "../../etc/passwd" must not read outside it.
func (a *Adapter) resolve(file string) (string, error) {
	if file == "" {
		return "", errors.New("csv source requires a file parameter")
	}

	base, err := filepath.Abs(a.dir)
	if err != nil {
		return "", fmt.Errorf("resolve csv dir: %w", err)
	}

	path := filepath.Join(base, filepath.Clean("/"+file))
	if !strings.HasPrefix(path, base+string(os.PathSeparator)) {
		return "", fmt.Errorf("csv file %q escapes the import directory", file)
	}
	return path, nil
}

func indexColumns(header []string) map[string]int {
	cols := make(map[string]int)

	for i, h := range header {
		h := normalizeHeader(h)

		for canonical, names := range aliases {
			if _, taken := cols[canonical]; taken {
				continue
			}
			for _, n := range names {
				if h == n {
					cols[canonical] = i
					break
				}
			}
		}
	}
	return cols
}

// normalizeHeader folds the spellings a spreadsheet export actually produces
// ("Business Name", "business-name", "BUSINESS NAME") onto one canonical form,
// so the alias table does not have to enumerate every separator.
func normalizeHeader(h string) string {
	// Excel writes a UTF-8 BOM onto the first header cell.
	h = strings.TrimPrefix(h, "\ufeff")
	h = strings.ToLower(strings.TrimSpace(h))
	h = strings.NewReplacer(" ", "_", "-", "_", ".", "_").Replace(h)
	return strings.Trim(h, "_")
}

func toBusiness(rec []string, cols map[string]int, fallbackCategory string) (*domain.Business, error) {
	get := func(field string) string {
		i, ok := cols[field]
		if !ok || i >= len(rec) {
			return ""
		}
		return strings.TrimSpace(rec[i])
	}

	name := get("name")
	if name == "" {
		return nil, errors.New("missing business name")
	}

	b := &domain.Business{
		Name:      name,
		Address:   get("address"),
		Phone:     get("phone"),
		Website:   normalizeWebsite(get("website")),
		Category:  get("category"),
		Source:    "csv",
		SourceKey: get("key"),
	}
	if b.Category == "" {
		b.Category = fallbackCategory
	}
	// Without a stable key the dedup index cannot fire, so synthesise one from
	// the fields that identify the business.
	if b.SourceKey == "" {
		b.SourceKey = syntheticKey(b)
	}

	if r := get("rating"); r != "" {
		if v, err := strconv.ParseFloat(r, 64); err == nil {
			b.Rating = v
		}
	}

	lat, lng := get("lat"), get("lng")
	if lat != "" && lng != "" {
		latV, errLat := strconv.ParseFloat(lat, 64)
		lngV, errLng := strconv.ParseFloat(lng, 64)
		if errLat == nil && errLng == nil {
			b.Coordinates = &domain.Coordinates{Lat: latV, Lng: lngV}
		}
	}

	return b, nil
}

func syntheticKey(b *domain.Business) string {
	parts := []string{strings.ToLower(b.Name)}
	if b.Phone != "" {
		parts = append(parts, b.Phone)
	} else if b.Address != "" {
		parts = append(parts, strings.ToLower(b.Address))
	}
	return strings.Join(parts, "|")
}

func normalizeWebsite(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "-" || strings.EqualFold(raw, "n/a") || strings.EqualFold(raw, "none") {
		return ""
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	return raw
}

// send respects cancellation so a stalled consumer cannot block the producer
// forever.
func send(ctx context.Context, ch chan<- ports.BusinessResult, r ports.BusinessResult) bool {
	select {
	case ch <- r:
		return true
	case <-ctx.Done():
		return false
	}
}
