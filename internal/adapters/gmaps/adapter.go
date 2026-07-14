// Package gmaps scrapes Google Maps for real businesses by driving
// gosom/google-maps-scraper, which automates a headless Chromium and needs no
// API key.
//
// It is run as a subprocess rather than imported as a library: its internals
// (scrapemate, playwright) are not a stable public API, and vendoring Playwright
// into this module would make every build pull down a browser toolchain even
// when nobody is scraping Maps. Talking to it over a CSV file keeps the two
// programs independent.
//
// The headless browser is heavy. gosom's own Kubernetes example asks for 512Mi
// per instance, so on a small machine keep GMAPS_CONCURRENCY at 1.
package gmaps

import (
	"bufio"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/makeforme/leadscraper/internal/domain"
	"github.com/makeforme/leadscraper/internal/ports"
	"github.com/makeforme/leadscraper/pkg/config"
)

type Adapter struct {
	cfg config.GmapsConfig
	log *slog.Logger
}

func NewAdapter(cfg config.GmapsConfig, log *slog.Logger) *Adapter {
	return &Adapter{cfg: cfg, log: log}
}

func (a *Adapter) Name() string { return "google_maps" }

// Available reports whether the scraper can actually run, so main can decline to
// register a source that would fail every job it accepted.
func (a *Adapter) Available() error {
	switch a.cfg.Mode {
	case "docker":
		if _, err := exec.LookPath("docker"); err != nil {
			return errors.New("GMAPS_MODE=docker but the docker CLI is not on PATH")
		}

		// Having the client is not the same as being able to reach the daemon.
		// The usual cause is this process running inside a container without
		// /var/run/docker.sock, or without permission on it — and finding that
		// out now beats failing every scrape job later.
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		if out, err := exec.CommandContext(ctx, "docker", "info", "--format", "{{.ServerVersion}}").CombinedOutput(); err != nil {
			return fmt.Errorf("cannot reach the Docker daemon (%s). "+
				"If the worker is running in a container, mount /var/run/docker.sock and make sure "+
				"the container user can read it; otherwise run the worker natively (make run-worker)",
				strings.TrimSpace(lastLine(string(out))))
		}

	case "binary":
		if _, err := exec.LookPath(a.cfg.Binary); err != nil {
			return fmt.Errorf("google-maps-scraper binary %q is not on PATH; "+
				"install it or set GMAPS_MODE=docker", a.cfg.Binary)
		}

	default:
		return fmt.Errorf("unsupported GMAPS_MODE %q (want binary or docker)", a.cfg.Mode)
	}
	return nil
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) == 0 {
		return ""
	}
	return lines[len(lines)-1]
}

func (a *Adapter) Scrape(ctx context.Context, params ports.ScrapeParams, results chan<- ports.BusinessResult) error {
	defer close(results)

	if err := a.Available(); err != nil {
		return err
	}

	query := strings.TrimSpace(params.SearchQuery())
	if query == "" {
		return errors.New("google_maps needs a category or query")
	}

	// gosom writes its output to a file, so give it a private directory that is
	// cleaned up regardless of how this returns.
	workDir, err := os.MkdirTemp(a.cfg.WorkDir, "gmaps-*")
	if err != nil {
		return fmt.Errorf("create work dir: %w", err)
	}
	defer os.RemoveAll(workDir)

	queryFile := filepath.Join(workDir, "queries.txt")
	outFile := filepath.Join(workDir, "results.csv")

	if err := os.WriteFile(queryFile, []byte(query+"\n"), 0o600); err != nil {
		return fmt.Errorf("write query file: %w", err)
	}

	if err := a.run(ctx, workDir, queryFile, outFile, params.Limit); err != nil {
		return err
	}

	return a.stream(ctx, outFile, params, results)
}

func (a *Adapter) run(ctx context.Context, workDir, queryFile, outFile string, limit int) error {
	// The scrape is a long, browser-driven crawl. Bound it so a wedged Chromium
	// cannot hold a worker slot forever.
	ctx, cancel := context.WithTimeout(ctx, a.cfg.Timeout)
	defer cancel()

	cmd := a.command(ctx, workDir, queryFile, outFile, limit)

	a.log.Info("starting google maps scrape",
		"mode", a.cfg.Mode,
		"concurrency", a.cfg.Concurrency,
		"depth", a.cfg.Depth,
		"timeout", a.cfg.Timeout)

	// gosom reports progress on stderr. Surfacing it is the difference between
	// "it is working" and "it has hung", on a job that can run for many minutes.
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("pipe stderr: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start google-maps-scraper: %w", err)
	}

	var tail []string
	scanner := bufio.NewScanner(stderr)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		a.log.Debug("google-maps-scraper", "out", line)

		// Keep the last few lines so a failure can report why, not just a code.
		tail = append(tail, line)
		if len(tail) > 10 {
			tail = tail[1:]
		}
	}

	if err := cmd.Wait(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("google-maps-scraper timed out after %s "+
				"(raise GMAPS_TIMEOUT, or lower GMAPS_DEPTH): %s",
				a.cfg.Timeout, lastLines(tail))
		}

		// The single most likely failure, and the message gosom emits for it is
		// impenetrable. Say what to actually do about it.
		if joined := strings.Join(tail, " "); strings.Contains(joined, "could not install driver") {
			return fmt.Errorf("google-maps-scraper could not start its browser: "+
				"the Playwright-based images pin a driver version that Microsoft's "+
				"retired CDN no longer serves. Use the go-rod build instead: "+
				"GMAPS_DOCKER_IMAGE=gosom/google-maps-scraper:latest-rod (currently %q)",
				a.cfg.DockerImage)
		}

		return fmt.Errorf("google-maps-scraper failed: %w: %s", err, lastLines(tail))
	}

	return nil
}

// lastLines trims gosom's stderr down to something a log line can carry. Its
// banner alone is eight lines of box-drawing characters.
func lastLines(tail []string) string {
	var kept []string
	for _, line := range tail {
		if strings.ContainsAny(line, "╔╗╚╝║═") {
			continue
		}
		kept = append(kept, line)
	}
	if len(kept) > 3 {
		kept = kept[len(kept)-3:]
	}
	return strings.Join(kept, " | ")
}

func (a *Adapter) command(ctx context.Context, workDir, queryFile, outFile string, limit int) *exec.Cmd {
	depth := a.cfg.Depth
	// depth is how far the results list is scrolled. Asking for many results
	// with depth 1 would silently return only the first page.
	if limit > 20 && depth < 2 {
		depth = 2
	}

	if a.cfg.Mode == "docker" {
		// Deliberately no `-v gmaps-playwright-cache:/opt`, which gosom's README
		// suggests: mounting a volume over /opt hides the browser that is already
		// baked into the image, and the container then fails to start.
		args := []string{
			"run", "--rm",
			"-v", a.hostPath(workDir) + ":/work",
			a.cfg.DockerImage,
			"-input", "/work/queries.txt",
			"-results", "/work/results.csv",
		}
		args = append(args, a.flags(depth)...)
		return exec.CommandContext(ctx, "docker", args...)
	}

	args := []string{"-input", queryFile, "-results", outFile}
	args = append(args, a.flags(depth)...)

	cmd := exec.CommandContext(ctx, a.cfg.Binary, args...)
	cmd.Dir = workDir
	return cmd
}

// hostPath translates a path we can see into the path the Docker daemon can see.
//
// `docker run -v X:/work` is resolved by the daemon on the host, not by us. When
// the worker itself runs in a container, our /app/data/gmaps/gmaps-123 means
// nothing to the host, and the scraper would launch against an empty directory
// and find no query file. GMAPS_HOST_WORK_DIR names the same directory as the
// host sees it, so we can rewrite the prefix.
func (a *Adapter) hostPath(workDir string) string {
	if a.cfg.HostWorkDir == "" || a.cfg.WorkDir == "" {
		return workDir
	}

	rel, err := filepath.Rel(a.cfg.WorkDir, workDir)
	if err != nil || strings.HasPrefix(rel, "..") {
		// The temp dir is not under WorkDir after all; a rewritten path would be
		// worse than the original.
		return workDir
	}

	// The host is Linux even when this code is not, so join with forward slashes.
	return path.Join(a.cfg.HostWorkDir, filepath.ToSlash(rel))
}

func (a *Adapter) flags(depth int) []string {
	args := []string{
		"-depth", strconv.Itoa(depth),
		"-c", strconv.Itoa(a.cfg.Concurrency),
		"-lang", a.cfg.Lang,
		// Without this the scraper waits indefinitely for more work and never
		// exits, so the job would hang until our own timeout killed it.
		"-exit-on-inactivity", a.cfg.ExitOnInactivity.String(),
	}
	if a.cfg.ExtractEmail {
		args = append(args, "-email")
	}
	return args
}

// ---------------------------------------------------------------- parsing

// gosom emits 36+ columns. We take the ones the pipeline uses and keep a few
// more as metadata; the rest are ignored.
const (
	colTitle    = "title"
	colCategory = "category"
	colAddress  = "address"
	colFullAddr = "complete_address"
	colPhone    = "phone"
	colWebsite  = "website"
	colRating   = "review_rating"
	colReviews  = "review_count"
	colLat      = "latitude"
	colLng      = "longitude"
	colPlaceID  = "place_id"
	colCID      = "cid"
	colLink     = "link"
	colStatus   = "status"
	colEmails   = "emails"
)

func (a *Adapter) stream(ctx context.Context, outFile string, params ports.ScrapeParams, results chan<- ports.BusinessResult) error {
	f, err := os.Open(outFile)
	if err != nil {
		if os.IsNotExist(err) {
			// The scraper exited cleanly but wrote nothing. That is a genuine
			// "no results", not a failure.
			a.log.Warn("google maps scrape produced no results file", "query", params.SearchQuery())
			return nil
		}
		return fmt.Errorf("open scraper output: %w", err)
	}
	defer f.Close()

	reader := csv.NewReader(f)
	reader.FieldsPerRecord = -1
	reader.LazyQuotes = true

	header, err := reader.Read()
	if errors.Is(err, io.EOF) {
		a.log.Warn("google maps scrape returned no rows", "query", params.SearchQuery())
		return nil
	}
	if err != nil {
		return fmt.Errorf("read scraper header: %w", err)
	}

	cols := index(header)
	if _, ok := cols[colTitle]; !ok {
		return fmt.Errorf("scraper output has no %q column (got %v)", colTitle, header)
	}

	sent, skipped := 0, 0
	for {
		if params.Limit > 0 && sent >= params.Limit {
			break
		}

		rec, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			skipped++
			continue
		}

		b := toBusiness(rec, cols, params.Category)
		if b == nil {
			skipped++
			continue
		}

		select {
		case results <- ports.BusinessResult{Business: *b}:
			sent++
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	a.log.Info("google maps scrape finished",
		"query", params.SearchQuery(), "found", sent, "skipped", skipped)
	return nil
}

func index(header []string) map[string]int {
	cols := make(map[string]int, len(header))
	for i, h := range header {
		h = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(h, "\ufeff")))
		if _, taken := cols[h]; !taken {
			cols[h] = i
		}
	}
	return cols
}

func toBusiness(rec []string, cols map[string]int, fallbackCategory string) *domain.Business {
	get := func(col string) string {
		i, ok := cols[col]
		if !ok || i >= len(rec) {
			return ""
		}
		return strings.TrimSpace(rec[i])
	}

	name := get(colTitle)
	if name == "" {
		return nil
	}

	// A permanently closed business is not a lead.
	if status := strings.ToLower(get(colStatus)); strings.Contains(status, "permanently closed") {
		return nil
	}

	address := cleanAddress(get(colAddress), get(colFullAddr))

	category := get(colCategory)
	if category == "" {
		category = fallbackCategory
	}

	b := &domain.Business{
		Name:     name,
		Address:  address,
		Phone:    get(colPhone),
		Website:  normalizeWebsite(get(colWebsite)),
		Category: category,
		Source:   "google_maps",
		Rating:   parseFloat(get(colRating)),
	}

	// place_id is Google's stable identifier, so it is what dedup keys on. cid
	// is the fallback; a name+address hash is the last resort.
	b.SourceKey = firstNonEmpty(get(colPlaceID), get(colCID), strings.ToLower(name+"|"+address))

	if lat, lng := parseFloat(get(colLat)), parseFloat(get(colLng)); lat != 0 || lng != 0 {
		b.Coordinates = &domain.Coordinates{Lat: lat, Lng: lng}
	}

	// Review count is the signal that tells you whether a business is actually
	// alive, and gosom's -email flag output is worth keeping too.
	b.Metadata = metadata(map[string]any{
		"review_count": int(parseFloat(get(colReviews))),
		"maps_url":     get(colLink),
		"status":       get(colStatus),
		"emails":       splitList(get(colEmails)),
	})

	return b
}

// completeAddress is gosom's `complete_address` column, which is a JSON object
// rather than a string — the one column in its output that is not plain text.
type completeAddress struct {
	Street     string `json:"street"`
	Borough    string `json:"borough"`
	City       string `json:"city"`
	PostalCode string `json:"postal_code"`
	State      string `json:"state"`
	Country    string `json:"country"`
}

// cleanAddress prefers the plain `address` column, which is already the human
// string Google shows.
//
// Preferring `complete_address` looked like the richer choice and was not: it is
// a JSON object, so every address was stored as a raw `{"borough":...}` blob.
// That is unreadable in the UI and useless in a CSV export. It is only worth
// falling back to, and only after being flattened into a sentence.
func cleanAddress(plain, complete string) string {
	if plain = strings.TrimSpace(plain); plain != "" && !strings.HasPrefix(plain, "{") {
		return plain
	}

	if complete = strings.TrimSpace(complete); complete == "" {
		return plain
	}

	var c completeAddress
	if err := json.Unmarshal([]byte(complete), &c); err != nil {
		// Not the JSON we expected. A plain string here is still better than
		// nothing, but a stray brace is not.
		if strings.HasPrefix(complete, "{") {
			return ""
		}
		return complete
	}

	parts := make([]string, 0, 5)
	for _, p := range []string{c.Street, c.Borough, c.City, c.State, c.PostalCode} {
		if p = strings.TrimSpace(p); p != "" {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, ", ")
}

func normalizeWebsite(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "-" {
		return ""
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	return raw
}

func parseFloat(s string) float64 {
	// gosom writes review counts as "1,234" in some locales.
	s = strings.ReplaceAll(strings.TrimSpace(s), ",", "")
	if s == "" {
		return 0
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return v
}

func splitList(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ';' || r == '|' })
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// metadata fills the JSONB column. A marshal failure must not lose the business,
// so it degrades to no metadata rather than an error.
func metadata(m map[string]any) []byte {
	raw, err := json.Marshal(m)
	if err != nil {
		return nil
	}
	return raw
}
