package workers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/makeforme/leadscraper/internal/ai"
	"github.com/makeforme/leadscraper/internal/crawler"
	"github.com/makeforme/leadscraper/internal/domain"
	"github.com/makeforme/leadscraper/internal/extract"
	"github.com/makeforme/leadscraper/internal/ports"
	"github.com/makeforme/leadscraper/internal/queue"
	"github.com/makeforme/leadscraper/internal/scoring"
	"github.com/makeforme/leadscraper/pkg/config"
)

// Deps is everything the handlers need. Passing one struct keeps the worker
// constructor from growing a dozen positional arguments.
type Deps struct {
	Businesses   ports.BusinessRepository
	Websites     ports.WebsiteRepository
	Contacts     ports.ContactRepository
	Socials      ports.SocialProfileRepository
	Audits       ports.AuditRepository
	Scores       ports.LeadScoreRepository
	Jobs         ports.ScrapeJobRepository
	CrawlResults ports.CrawlResultRepository
	Technologies ports.TechnologyRepository

	Sources map[string]ports.SourceAdapter
	Crawler *crawler.Crawler
	AI      ai.Provider
	Scoring *scoring.Engine
	Queue   queue.Queue

	CrawlerCfg config.CrawlerConfig
	AICfg      config.AIConfig
}

// collectPayload is the body of a collect_business job.
type collectPayload struct {
	ScrapeJobID string `json:"scrape_job_id"`
	Source      string `json:"source"`
	Category    string `json:"category"`
	Location    string `json:"location"`
	Query       string `json:"query"`
	File        string `json:"file"`
	Limit       int    `json:"limit"`
}

// collectBusiness runs a source adapter, stores what it finds, and fans out a
// crawl job per business that has a website.
func (w *Worker) collectBusiness(ctx context.Context, job *queue.Job) error {
	var p collectPayload
	if err := json.Unmarshal(job.Payload, &p); err != nil {
		return fmt.Errorf("decode collect payload: %w", err)
	}

	adapter, ok := w.deps.Sources[p.Source]
	if !ok {
		return fmt.Errorf("unknown source %q", p.Source)
	}

	log := w.log.With("scrape_job", p.ScrapeJobID, "source", p.Source)
	w.markJobRunning(ctx, p.ScrapeJobID)

	results := make(chan ports.BusinessResult)
	scrapeErr := make(chan error, 1)

	go func() {
		scrapeErr <- adapter.Scrape(ctx, ports.ScrapeParams{
			Category: p.Category,
			Location: p.Location,
			Limit:    p.Limit,
			Query:    p.Query,
			File:     p.File,
		}, results)
	}()

	var (
		batch  []*domain.Business
		failed int
	)
	for res := range results {
		if res.Err != nil {
			// One bad record should not sink the run; count it and carry on.
			failed++
			log.Warn("source produced an error", "error", res.Err)
			continue
		}
		b := res.Business
		batch = append(batch, &b)
	}

	// The adapter closes the channel when it is done, so by here the goroutine
	// has already sent its result.
	if err := <-scrapeErr; err != nil {
		w.failJob(ctx, p.ScrapeJobID, err)
		return fmt.Errorf("source %s: %w", p.Source, err)
	}

	inserted, err := w.deps.Businesses.BulkInsert(ctx, batch)
	if err != nil {
		w.failJob(ctx, p.ScrapeJobID, err)
		return fmt.Errorf("store businesses: %w", err)
	}

	// Every business needs scoring. Those with a website get crawled first, and
	// the crawl chain ends in a scoring job; the rest are scored immediately.
	queued := 0
	for _, b := range batch {
		next := queue.Job{Type: queue.JobRuleScoring, BusinessID: b.ID}
		if b.Website != "" {
			next = queue.Job{Type: queue.JobWebsiteCrawl, BusinessID: b.ID, URL: b.Website}
		}
		if err := w.deps.Queue.Enqueue(ctx, next); err != nil {
			log.Error("enqueue follow-up failed", "business_id", b.ID, "error", err)
			continue
		}
		queued++
	}

	log.Info("collection finished",
		"found", len(batch), "new", inserted, "failed", failed, "queued", queued)

	w.completeJob(ctx, p.ScrapeJobID, len(batch), inserted, failed)
	return nil
}

// crawlWebsite fetches the site and, while the HTML is still in memory, runs
// every extractor over it. Re-reading the pages from Postgres for each extract
// job would cost more than the crawl did.
func (w *Worker) crawlWebsite(ctx context.Context, job *queue.Job) error {
	if job.BusinessID == "" || job.URL == "" {
		return errors.New("website_crawl needs a business_id and a url")
	}

	res, err := w.deps.Crawler.Crawl(ctx, job.URL)
	if err != nil {
		return fmt.Errorf("crawl %s: %w", job.URL, err)
	}

	site := &domain.Website{
		BusinessID:       job.BusinessID,
		URL:              res.BaseURL,
		StatusCode:       res.StatusCode,
		LoadTimeMs:       res.LoadTimeMs,
		HasSSL:           res.HasSSL,
		HasBooking:       res.HasBooking,
		IsMobileFriendly: res.HasViewport,
		PagesCrawled:     len(res.Pages),
		Title:            res.Title,
		MetaDescription:  res.MetaDesc,
	}
	now := time.Now().UTC()
	site.CrawledAt = &now

	if err := w.deps.Websites.Create(ctx, site); err != nil {
		return fmt.Errorf("store website: %w", err)
	}

	// An unreachable site is a finding, not a failure: "their website is down"
	// is one of the strongest sales signals we have. Record it and score it.
	if !res.Reachable {
		w.log.Info("website unreachable, scoring anyway",
			"business_id", job.BusinessID, "url", job.URL, "error", res.Error)

		if err := w.storeCrawlResult(ctx, site.ID, res, job.URL); err != nil {
			return err
		}
		return w.deps.Queue.Enqueue(ctx, queue.Job{
			Type: queue.JobRuleScoring, BusinessID: job.BusinessID,
		})
	}

	if err := w.storeCrawlResult(ctx, site.ID, res, job.URL); err != nil {
		return err
	}

	if err := w.extractAll(ctx, job.BusinessID, site.ID, res); err != nil {
		return err
	}

	// Hand off to the AI audit when one is configured; it chains into scoring.
	// Otherwise go straight to scoring.
	next := queue.Job{Type: queue.JobRuleScoring, BusinessID: job.BusinessID}
	if w.hasAI() {
		next = queue.Job{
			Type:       queue.JobAIAudit,
			BusinessID: job.BusinessID,
			WebsiteID:  site.ID,
			URL:        res.BaseURL,
		}
	}
	return w.deps.Queue.Enqueue(ctx, next)
}

// extractAll persists contacts, socials, and technologies from a crawl.
func (w *Worker) extractAll(ctx context.Context, businessID, websiteID string, res *crawler.Result) error {
	var (
		contacts []*domain.Contact
		socials  []*domain.SocialProfile
		techs    []domain.Technology
		seenTech = map[string]bool{}
	)

	for _, page := range res.Pages {
		if page.Err != nil {
			continue
		}

		contacts = append(contacts, extract.Contacts(businessID, page.HTML, "crawl")...)
		socials = append(socials, extract.Socials(businessID, page.Links)...)
		socials = append(socials, extract.SocialsFromHTML(businessID, page.HTML)...)

		for _, t := range extract.Technologies(page.HTML, page.Headers) {
			if !seenTech[t.Name] {
				seenTech[t.Name] = true
				techs = append(techs, t)
			}
		}
	}

	if err := w.deps.Contacts.BulkUpsert(ctx, dedupeContacts(contacts)); err != nil {
		return fmt.Errorf("store contacts: %w", err)
	}
	if err := w.deps.Socials.BulkUpsert(ctx, dedupeSocials(socials)); err != nil {
		return fmt.Errorf("store socials: %w", err)
	}
	if err := w.deps.Technologies.ReplaceForWebsite(ctx, websiteID, techs); err != nil {
		return fmt.Errorf("store technologies: %w", err)
	}

	w.log.Debug("extraction finished",
		"business_id", businessID,
		"contacts", len(contacts), "socials", len(socials), "technologies", len(techs))

	return nil
}

func (w *Worker) storeCrawlResult(ctx context.Context, websiteID string, res *crawler.Result, url string) error {
	landing := crawler.Page{URL: url}
	if len(res.Pages) > 0 {
		landing = res.Pages[0]
	}

	cr := &domain.CrawlResult{
		WebsiteID:  websiteID,
		URL:        landing.URL,
		StatusCode: landing.StatusCode,
		Title:      landing.Title,
		MetaTags:   landing.MetaTags,
		Links:      landing.Links,
		Error:      res.Error,
	}

	// Storing raw HTML is optional: it is by far the largest column in the
	// schema, and this box does not have the disk to keep it by default.
	if w.deps.CrawlerCfg.StoreHTML {
		cr.HTML = landing.HTML
	}

	if err := w.deps.CrawlResults.Create(ctx, cr); err != nil {
		return fmt.Errorf("store crawl result: %w", err)
	}
	return nil
}

// The standalone extract jobs re-run a single extractor against stored HTML.
// They are for reprocessing an existing crawl and need CRAWLER_STORE_HTML=true.

func (w *Worker) extractContacts(ctx context.Context, job *queue.Job) error {
	cr, err := w.storedCrawl(ctx, job)
	if err != nil {
		return err
	}
	contacts := extract.Contacts(job.BusinessID, cr.HTML, "recrawl")
	if err := w.deps.Contacts.BulkUpsert(ctx, dedupeContacts(contacts)); err != nil {
		return fmt.Errorf("store contacts: %w", err)
	}
	w.log.Info("contacts extracted", "business_id", job.BusinessID, "count", len(contacts))
	return nil
}

func (w *Worker) findSocials(ctx context.Context, job *queue.Job) error {
	cr, err := w.storedCrawl(ctx, job)
	if err != nil {
		return err
	}
	socials := append(
		extract.Socials(job.BusinessID, cr.Links),
		extract.SocialsFromHTML(job.BusinessID, cr.HTML)...,
	)
	if err := w.deps.Socials.BulkUpsert(ctx, dedupeSocials(socials)); err != nil {
		return fmt.Errorf("store socials: %w", err)
	}
	w.log.Info("socials extracted", "business_id", job.BusinessID, "count", len(socials))
	return nil
}

func (w *Worker) extractTechnology(ctx context.Context, job *queue.Job) error {
	cr, err := w.storedCrawl(ctx, job)
	if err != nil {
		return err
	}
	// Headers are not persisted, so a reprocess sees only the HTML fingerprints.
	techs := extract.Technologies(cr.HTML, nil)
	if err := w.deps.Technologies.ReplaceForWebsite(ctx, job.WebsiteID, techs); err != nil {
		return fmt.Errorf("store technologies: %w", err)
	}
	w.log.Info("technologies extracted", "website_id", job.WebsiteID, "count", len(techs))
	return nil
}

func (w *Worker) storedCrawl(ctx context.Context, job *queue.Job) (*domain.CrawlResult, error) {
	if job.WebsiteID == "" {
		return nil, errors.New("job needs a website_id")
	}

	cr, err := w.deps.CrawlResults.GetByWebsiteID(ctx, job.WebsiteID)
	if err != nil {
		return nil, fmt.Errorf("load crawl result: %w", err)
	}
	if cr.HTML == "" {
		return nil, errors.New("stored crawl has no HTML; set CRAWLER_STORE_HTML=true to reprocess")
	}
	return cr, nil
}

// aiAudit sends the page to the model and stores the report. The site has
// already been crawled, so this reuses what is on disk rather than refetching.
func (w *Worker) aiAudit(ctx context.Context, job *queue.Job) error {
	if !w.hasAI() {
		// Not an error: an unconfigured provider means "skip", not "retry".
		w.log.Debug("no AI provider, skipping audit", "business_id", job.BusinessID)
		return w.deps.Queue.Enqueue(ctx, queue.Job{
			Type: queue.JobRuleScoring, BusinessID: job.BusinessID,
		})
	}

	business, err := w.deps.Businesses.GetByID(ctx, job.BusinessID)
	if err != nil {
		return fmt.Errorf("load business: %w", err)
	}

	site, err := w.deps.Websites.GetByBusinessID(ctx, job.BusinessID)
	if err != nil {
		return fmt.Errorf("load website: %w", err)
	}

	req := ai.AuditRequest{
		URL:          site.URL,
		Title:        site.Title,
		StatusCode:   site.StatusCode,
		MetaTags:     map[string]string{"description": site.MetaDescription},
		BusinessName: business.Name,
		Category:     business.Category,
		HasSSL:       site.HasSSL,
		HasBooking:   site.HasBooking,
		IsMobile:     site.IsMobileFriendly,
	}

	if techs, err := w.deps.Technologies.GetByWebsiteID(ctx, site.ID); err == nil {
		for _, t := range techs {
			req.Technologies = append(req.Technologies, t.Name)
		}
	}

	// Send visible text, not raw HTML. Markup is most of a page's bytes and
	// almost none of its meaning, and we pay per token.
	if cr, err := w.deps.CrawlResults.GetByWebsiteID(ctx, site.ID); err == nil && cr.HTML != "" {
		req.HTMLContent = extract.StripTags(cr.HTML)
	} else {
		req.HTMLContent = strings.TrimSpace(site.Title + "\n" + site.MetaDescription)
	}

	report, err := w.deps.AI.AuditWebsite(ctx, req)
	if err != nil {
		if errors.Is(err, ai.ErrNotConfigured) {
			return w.deps.Queue.Enqueue(ctx, queue.Job{
				Type: queue.JobRuleScoring, BusinessID: job.BusinessID,
			})
		}
		return fmt.Errorf("ai audit: %w", err)
	}

	err = w.deps.Audits.Create(ctx, &domain.AuditReport{
		WebsiteID:       site.ID,
		BusinessID:      job.BusinessID,
		QualityScore:    report.QualityScore,
		SEOScore:        report.SEOScore,
		MobileScore:     report.MobileScore,
		Issues:          nonNil(report.Issues),
		Recommendations: nonNil(report.Recommendations),
		Summary:         report.Summary,
		ServicesToOffer: nonNil(report.ServicesToOffer),
	})
	if err != nil {
		return fmt.Errorf("store audit report: %w", err)
	}

	w.log.Info("ai audit stored",
		"business_id", job.BusinessID,
		"quality", report.QualityScore, "seo", report.SEOScore, "mobile", report.MobileScore)

	return w.deps.Queue.Enqueue(ctx, queue.Job{
		Type: queue.JobRuleScoring, BusinessID: job.BusinessID,
	})
}

// ruleScoring assembles everything known about a business and scores it.
func (w *Worker) ruleScoring(ctx context.Context, job *queue.Job) error {
	if job.BusinessID == "" {
		return errors.New("rule_scoring needs a business_id")
	}

	business, err := w.deps.Businesses.GetByID(ctx, job.BusinessID)
	if err != nil {
		return fmt.Errorf("load business: %w", err)
	}

	evalCtx := &scoring.EvalContext{
		HasWebsite: business.Website != "",
		HasPhone:   business.Phone != "",
	}

	// A website row exists only if a crawl ran, so its absence is not an error.
	site, err := w.deps.Websites.GetByBusinessID(ctx, job.BusinessID)
	if err == nil {
		evalCtx.IsReachable = site.StatusCode > 0 && site.StatusCode < 400
		evalCtx.HasSSL = site.HasSSL
		evalCtx.HasBooking = site.HasBooking
		evalCtx.IsMobileFriendly = site.IsMobileFriendly
		evalCtx.HasMetaTags = site.MetaDescription != ""
		evalCtx.HasContactForm = site.HasBooking // best available proxy without re-parsing
	}

	contacts, err := w.deps.Contacts.GetByBusinessID(ctx, job.BusinessID)
	if err == nil {
		for _, c := range contacts {
			if c.Email != "" {
				evalCtx.HasEmail = true
			}
			if c.Phone != "" || c.WhatsApp != "" {
				evalCtx.HasPhone = true
			}
		}
	}

	socials, err := w.deps.Socials.GetByBusinessID(ctx, job.BusinessID)
	if err == nil {
		evalCtx.HasSocialLinks = len(socials) > 0
	}

	result := w.deps.Scoring.Evaluate(evalCtx)

	score := &domain.LeadScore{
		BusinessID: job.BusinessID,
		RuleScore:  result.TotalScore,
		TotalScore: result.Percent(),
		Priority:   result.Priority(),
		Breakdown:  breakdownToMap(result.Breakdown),
	}

	// An AI audit, when one exists, adjusts the total. A bad site (low quality)
	// means a better lead, so the AI score is inverted before it is folded in.
	if audit, err := w.deps.Audits.GetByBusinessID(ctx, job.BusinessID); err == nil {
		avg := (audit.QualityScore + audit.SEOScore + audit.MobileScore) / 3
		score.AIScore = (10 - avg) * 10
		score.TotalScore = (result.Percent()*7 + score.AIScore*3) / 10
	}

	score.SalesSuggestion = result.SalesSuggestion(business.Name)

	if err := w.deps.Scores.Create(ctx, score); err != nil {
		return fmt.Errorf("store lead score: %w", err)
	}

	w.log.Info("lead scored",
		"business_id", job.BusinessID, "business", business.Name,
		"total", score.TotalScore, "priority", score.Priority)

	return w.deps.Queue.Enqueue(ctx, queue.Job{
		Type: queue.JobGenRecommendation, BusinessID: job.BusinessID,
	})
}

// genRecommendation folds the AI's summary into the stored sales suggestion.
func (w *Worker) genRecommendation(ctx context.Context, job *queue.Job) error {
	if job.BusinessID == "" {
		return errors.New("gen_recommendation needs a business_id")
	}

	score, err := w.deps.Scores.GetByBusinessID(ctx, job.BusinessID)
	if err != nil {
		return fmt.Errorf("load lead score: %w", err)
	}

	audit, err := w.deps.Audits.GetByBusinessID(ctx, job.BusinessID)
	if err != nil {
		// No audit is fine; the rule-based suggestion already stands.
		w.log.Debug("no audit to fold in", "business_id", job.BusinessID)
		return nil
	}

	var b strings.Builder
	b.WriteString(score.SalesSuggestion)

	if audit.Summary != "" {
		b.WriteString("\n\nSite audit: ")
		b.WriteString(audit.Summary)
	}
	if len(audit.ServicesToOffer) > 0 {
		b.WriteString("\n\nPitch: ")
		b.WriteString(strings.Join(audit.ServicesToOffer, ", "))
	}
	if len(audit.Issues) > 0 {
		b.WriteString("\n\nTop issues: ")
		b.WriteString(strings.Join(topN(audit.Issues, 3), "; "))
	}

	score.SalesSuggestion = b.String()
	if err := w.deps.Scores.Update(ctx, score); err != nil {
		return fmt.Errorf("update sales suggestion: %w", err)
	}

	w.log.Info("recommendation generated", "business_id", job.BusinessID)
	return nil
}

// ---------- helpers ----------

func (w *Worker) hasAI() bool {
	if w.deps.AI == nil {
		return false
	}
	_, noop := w.deps.AI.(ai.Noop)
	return !noop
}

func (w *Worker) markJobRunning(ctx context.Context, id string) {
	if id == "" {
		return
	}
	job, err := w.deps.Jobs.GetByID(ctx, id)
	if err != nil {
		return
	}
	now := time.Now().UTC()
	job.Status = "running"
	job.StartedAt = &now
	w.logIfErr(w.deps.Jobs.Update(ctx, job), "mark scrape job running", id)
}

func (w *Worker) completeJob(ctx context.Context, id string, found, success, failed int) {
	if id == "" {
		return
	}
	job, err := w.deps.Jobs.GetByID(ctx, id)
	if err != nil {
		return
	}
	now := time.Now().UTC()
	job.Status = "completed"
	job.TotalFound = found
	job.SuccessCount = success
	job.FailCount = failed
	job.CompletedAt = &now
	w.logIfErr(w.deps.Jobs.Update(ctx, job), "complete scrape job", id)
}

func (w *Worker) failJob(ctx context.Context, id string, cause error) {
	if id == "" {
		return
	}
	job, err := w.deps.Jobs.GetByID(ctx, id)
	if err != nil {
		return
	}
	now := time.Now().UTC()
	job.Status = "failed"
	job.Error = cause.Error()
	job.CompletedAt = &now
	w.logIfErr(w.deps.Jobs.Update(ctx, job), "fail scrape job", id)
}

// logIfErr records bookkeeping failures without masking the real error the
// caller is already returning.
func (w *Worker) logIfErr(err error, what, id string) {
	if err != nil {
		w.log.Error(what+" failed", "scrape_job", id, "error", err, slog.String("kind", "bookkeeping"))
	}
}

func dedupeContacts(in []*domain.Contact) []*domain.Contact {
	seen := make(map[string]bool, len(in))
	out := in[:0:0]

	for _, c := range in {
		key := c.Email + "|" + c.Phone + "|" + c.WhatsApp
		if key == "||" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, c)
	}
	return out
}

func dedupeSocials(in []*domain.SocialProfile) []*domain.SocialProfile {
	// The schema is UNIQUE(business_id, platform), so a duplicate platform in
	// the same batch would make the upsert hit the same row twice.
	seen := make(map[string]bool, len(in))
	out := in[:0:0]

	for _, s := range in {
		if seen[s.Platform] {
			continue
		}
		seen[s.Platform] = true
		out = append(out, s)
	}
	return out
}

func breakdownToMap(b map[scoring.Rule]int) map[string]int {
	out := make(map[string]int, len(b))
	for k, v := range b {
		out[string(k)] = v
	}
	return out
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func topN(s []string, n int) []string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
