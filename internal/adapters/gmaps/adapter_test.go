package gmaps

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/makeforme/leadscraper/internal/ports"
	"github.com/makeforme/leadscraper/pkg/config"
)

func testAdapter() *Adapter {
	return NewAdapter(config.GmapsConfig{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// collect runs the parser over a canned gosom output file.
func collect(t *testing.T, csvBody string, params ports.ScrapeParams) []ports.BusinessResult {
	t.Helper()

	path := filepath.Join(t.TempDir(), "results.csv")
	if err := os.WriteFile(path, []byte(csvBody), 0o600); err != nil {
		t.Fatal(err)
	}

	results := make(chan ports.BusinessResult, 32)
	errc := make(chan error, 1)
	go func() {
		errc <- testAdapter().stream(context.Background(), path, params, results)
		close(results)
	}()

	var out []ports.BusinessResult
	for r := range results {
		out = append(out, r)
	}
	if err := <-errc; err != nil {
		t.Fatalf("stream() error = %v", err)
	}
	return out
}

// A trimmed version of gosom's real 36-column output.
const gosomCSV = `input_id,link,title,category,address,website,phone,review_count,review_rating,latitude,longitude,cid,status,place_id,complete_address,emails
1,https://maps.google.com/?cid=123,Sweet Crumbs Bakery,Bakery,"MG Road, Kochi",sweetcrumbs.in,+91 98765 43210,"1,204",4.6,9.9312,76.2673,123,Open,ChIJabc123,"12 MG Road, Kochi, Kerala 682011",hi@sweetcrumbs.in
2,https://maps.google.com/?cid=456,Closed Cafe,Cafe,"Fort Kochi",,+91 98765 43211,12,3.1,9.9650,76.2420,456,Permanently closed,ChIJdef456,"Fort Kochi, Kerala",
3,https://maps.google.com/?cid=789,No Website Sweets,Dessert shop,"Ernakulam",,9812345678,88,4.2,9.9816,76.2999,789,Open,ChIJghi789,"Ernakulam, Kerala",
`

func TestStreamMapsGosomColumns(t *testing.T) {
	got := collect(t, gosomCSV, ports.ScrapeParams{})

	// The permanently closed business is not a lead and must be dropped.
	if len(got) != 2 {
		t.Fatalf("expected 2 open businesses, got %d", len(got))
	}

	b := got[0].Business
	if b.Name != "Sweet Crumbs Bakery" {
		t.Errorf("Name = %q", b.Name)
	}
	// The plain `address` column wins. complete_address looks richer but is a
	// JSON object, and preferring it stored every address as a raw blob.
	if b.Address != "MG Road, Kochi" {
		t.Errorf("Address = %q, want the plain human-readable address column", b.Address)
	}
	if b.Phone != "+91 98765 43210" {
		t.Errorf("Phone = %q", b.Phone)
	}
	// A bare host must get a scheme or the crawler cannot fetch it.
	if b.Website != "https://sweetcrumbs.in" {
		t.Errorf("Website = %q, want a scheme added", b.Website)
	}
	if b.Rating != 4.6 {
		t.Errorf("Rating = %v", b.Rating)
	}
	if b.Category != "Bakery" {
		t.Errorf("Category = %q", b.Category)
	}
	if b.Source != "google_maps" {
		t.Errorf("Source = %q", b.Source)
	}
	// place_id is Google's stable id, so dedup keys on it.
	if b.SourceKey != "ChIJabc123" {
		t.Errorf("SourceKey = %q, want the place_id", b.SourceKey)
	}
	if b.Coordinates == nil || b.Coordinates.Lat != 9.9312 {
		t.Errorf("Coordinates = %+v", b.Coordinates)
	}

	// A business with no website is the strongest lead there is, so it must
	// survive with an empty website rather than being dropped.
	if second := got[1].Business; second.Website != "" {
		t.Errorf("expected an empty website, got %q", second.Website)
	}
}

func TestStreamParsesThousandsSeparatedReviewCount(t *testing.T) {
	// gosom writes "1,204" for the review count, which ParseFloat rejects.
	got := collect(t, gosomCSV, ports.ScrapeParams{})

	var meta map[string]any
	if err := json.Unmarshal(got[0].Business.Metadata, &meta); err != nil {
		t.Fatalf("metadata is not valid JSON: %v", err)
	}

	if meta["review_count"] != float64(1204) {
		t.Errorf("review_count = %v, want 1204 (the comma must be stripped)", meta["review_count"])
	}
}

func TestStreamHonoursLimit(t *testing.T) {
	if got := collect(t, gosomCSV, ports.ScrapeParams{Limit: 1}); len(got) != 1 {
		t.Errorf("expected the limit to cap results at 1, got %d", len(got))
	}
}

func TestStreamFallsBackToTheJobCategory(t *testing.T) {
	csvBody := "title,category,address\nMystery Shop,,Kochi\n"

	got := collect(t, csvBody, ports.ScrapeParams{Category: "bakery"})
	if len(got) != 1 {
		t.Fatalf("expected 1 row, got %d", len(got))
	}
	if got[0].Business.Category != "bakery" {
		t.Errorf("Category = %q, want the job's category when Maps gives none", got[0].Business.Category)
	}
}

func TestStreamTreatsAMissingFileAsNoResults(t *testing.T) {
	// The scraper can exit cleanly having found nothing. That is not a failure,
	// and must not fail the whole job.
	results := make(chan ports.BusinessResult, 1)
	err := testAdapter().stream(context.Background(),
		filepath.Join(t.TempDir(), "nope.csv"), ports.ScrapeParams{}, results)
	if err != nil {
		t.Fatalf("a missing output file should be an empty result, not an error: %v", err)
	}
}

func TestAvailableRejectsAnUnknownMode(t *testing.T) {
	a := NewAdapter(config.GmapsConfig{Mode: "wat"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := a.Available(); err == nil {
		t.Fatal("expected an unknown GMAPS_MODE to be rejected")
	}
}

// When the worker runs inside a container, `docker run -v X:/work` is resolved
// by the daemon on the HOST. Passing our own in-container path would bind-mount
// a directory the host does not have, and the scraper would launch against an
// empty dir and find no query file.
func TestHostPathTranslatesForASiblingContainer(t *testing.T) {
	a := NewAdapter(config.GmapsConfig{
		WorkDir:     "/app/data/gmaps",
		HostWorkDir: "/home/gokul/leadscraper/data/gmaps",
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))

	got := a.hostPath("/app/data/gmaps/gmaps-12345")
	want := "/home/gokul/leadscraper/data/gmaps/gmaps-12345"

	if got != want {
		t.Errorf("hostPath() = %q, want %q", got, want)
	}
}

func TestHostPathIsAPassThroughWhenRunningNatively(t *testing.T) {
	// No HostWorkDir means the worker is not containerised, so our path is
	// already the host's path and must be left alone.
	a := NewAdapter(config.GmapsConfig{}, slog.New(slog.NewTextHandler(io.Discard, nil)))

	if got := a.hostPath("/tmp/gmaps-999"); got != "/tmp/gmaps-999" {
		t.Errorf("hostPath() = %q, want it unchanged", got)
	}
}

func TestHostPathRefusesToRewriteAPathOutsideWorkDir(t *testing.T) {
	// A rewritten path that escapes the mapping would be worse than the original.
	a := NewAdapter(config.GmapsConfig{
		WorkDir:     "/app/data/gmaps",
		HostWorkDir: "/host/data/gmaps",
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))

	if got := a.hostPath("/somewhere/else"); got != "/somewhere/else" {
		t.Errorf("hostPath() = %q, want the unrelated path left alone", got)
	}
}

// gosom's complete_address column is a JSON object, not a string -- the one
// column in its output that is not plain text. Preferring it over the plain
// `address` column stored every address as a raw {"borough":...} blob.
func TestCleanAddressPrefersThePlainColumn(t *testing.T) {
	plain := "12 MG Road, Kochi, Kerala 682011"
	jsonBlob := `{"borough":"Kaloor","street":"CPRA 52","city":"Kochi","postal_code":"682017"}`

	if got := cleanAddress(plain, jsonBlob); got != plain {
		t.Errorf("cleanAddress() = %q, want the human-readable plain column %q", got, plain)
	}
}

func TestCleanAddressFlattensTheJSONFallback(t *testing.T) {
	// With no plain address, the JSON is worth using -- but as a sentence, not
	// as a blob.
	got := cleanAddress("", `{"borough":"Kaloor","street":"CPRA 52, Perandoor Rd","city":"Kochi","state":"Kerala","postal_code":"682017"}`)
	want := "CPRA 52, Perandoor Rd, Kaloor, Kochi, Kerala, 682017"

	if got != want {
		t.Errorf("cleanAddress() = %q, want %q", got, want)
	}
	if strings.Contains(got, "{") {
		t.Errorf("raw JSON leaked into the address: %q", got)
	}
}

func TestCleanAddressNeverReturnsRawJSON(t *testing.T) {
	// Even if the plain column itself somehow holds the blob.
	blob := `{"borough":"Kaloor","city":"Kochi"}`

	if got := cleanAddress(blob, blob); strings.Contains(got, "{") {
		t.Errorf("raw JSON must never reach the database, got %q", got)
	}
}

func TestCleanAddressHandlesUnparseableInput(t *testing.T) {
	if got := cleanAddress("", "not json at all"); got != "not json at all" {
		t.Errorf("a non-JSON fallback should pass through, got %q", got)
	}
}
