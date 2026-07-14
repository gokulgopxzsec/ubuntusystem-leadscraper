package csv

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/makeforme/leadscraper/internal/ports"
)

func collect(t *testing.T, dir string, params ports.ScrapeParams) ([]ports.BusinessResult, error) {
	t.Helper()

	results := make(chan ports.BusinessResult)
	errc := make(chan error, 1)

	go func() { errc <- NewAdapter(dir).Scrape(context.Background(), params, results) }()

	var out []ports.BusinessResult
	for r := range results {
		out = append(out, r)
	}
	return out, <-errc
}

func writeCSV(t *testing.T, body string) string {
	t.Helper()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "leads.csv"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestScrapeReadsAliasedHeaders(t *testing.T) {
	// Real exports never use the column names you would have chosen: they come
	// capitalised, space-separated, and under a different word entirely.
	dir := writeCSV(t, `Business Name,Full Address,Mobile,Site,Stars
Chai Cafe,"12 MG Road, Bengaluru",+91 98765 43210,chaicafe.in,4.5
`)

	got, err := collect(t, dir, ports.ScrapeParams{File: "leads.csv"})
	if err != nil {
		t.Fatalf("Scrape() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 row, got %d", len(got))
	}

	b := got[0].Business
	if b.Name != "Chai Cafe" {
		t.Errorf("name = %q, want the 'Business Name' column to map to name", b.Name)
	}
	if b.Phone != "+91 98765 43210" {
		t.Errorf("phone = %q, want the 'Mobile' column to map to phone", b.Phone)
	}
	if b.Website != "https://chaicafe.in" {
		t.Errorf("website = %q, want the 'Site' column to map to website", b.Website)
	}
	if b.Rating != 4.5 {
		t.Errorf("rating = %v, want the 'Stars' column to map to rating", b.Rating)
	}
}

func TestScrapeParsesRows(t *testing.T) {
	dir := writeCSV(t, `name,address,phone,website,rating,category
Chai Cafe,"12 MG Road, Bengaluru",+91 98765 43210,chaicafe.in,4.5,cafe
Dosa Corner,"5 Church St, Bengaluru",9812345678,,4.1,restaurant
`)

	got, err := collect(t, dir, ports.ScrapeParams{File: "leads.csv"})
	if err != nil {
		t.Fatalf("Scrape() error = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(got))
	}

	first := got[0].Business
	if first.Name != "Chai Cafe" {
		t.Errorf("name = %q", first.Name)
	}
	// A bare host must be given a scheme, or the crawler cannot fetch it.
	if first.Website != "https://chaicafe.in" {
		t.Errorf("website = %q, want a scheme to have been added", first.Website)
	}
	if first.Rating != 4.5 {
		t.Errorf("rating = %v", first.Rating)
	}
	// Without a source key the dedup index cannot fire on re-import.
	if first.SourceKey == "" {
		t.Error("expected a synthetic source key")
	}

	if second := got[1].Business; second.Website != "" {
		t.Errorf("an empty website cell should stay empty, got %q", second.Website)
	}
}

func TestScrapeSkipsBadRowsWithoutAbortingTheImport(t *testing.T) {
	dir := writeCSV(t, `name,phone
Chai Cafe,9876543210
,9812345678
Dosa Corner,9811111111
`)

	got, err := collect(t, dir, ports.ScrapeParams{File: "leads.csv"})
	if err != nil {
		t.Fatalf("Scrape() error = %v", err)
	}

	var ok, failed int
	for _, r := range got {
		if r.Err != nil {
			failed++
		} else {
			ok++
		}
	}

	if ok != 2 {
		t.Errorf("expected 2 good rows, got %d", ok)
	}
	if failed != 1 {
		t.Errorf("expected the nameless row to be reported, got %d errors", failed)
	}
}

func TestScrapeHonoursLimit(t *testing.T) {
	dir := writeCSV(t, `name
A
B
C
`)

	got, err := collect(t, dir, ports.ScrapeParams{File: "leads.csv", Limit: 2})
	if err != nil {
		t.Fatalf("Scrape() error = %v", err)
	}
	if len(got) != 2 {
		t.Errorf("expected the limit to cap results at 2, got %d", len(got))
	}
}

func TestScrapeRefusesPathTraversal(t *testing.T) {
	// The filename arrives over the API, so it must not be able to read
	// anything outside the import directory.
	_, err := collect(t, writeCSV(t, "name\nA\n"), ports.ScrapeParams{
		File: "../../../etc/passwd",
	})
	if err == nil {
		t.Fatal("expected a traversal attempt to be rejected")
	}
}

func TestScrapeRequiresANameColumn(t *testing.T) {
	dir := writeCSV(t, "phone,website\n9876543210,x.in\n")

	if _, err := collect(t, dir, ports.ScrapeParams{File: "leads.csv"}); err == nil {
		t.Fatal("expected an error when the file has no name column")
	}
}
