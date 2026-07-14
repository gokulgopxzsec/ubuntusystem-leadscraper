package workers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/makeforme/leadscraper/internal/adapters/db/postgres"
	"github.com/makeforme/leadscraper/internal/ai"
	"github.com/makeforme/leadscraper/internal/crawler"
	"github.com/makeforme/leadscraper/internal/domain"
	"github.com/makeforme/leadscraper/internal/embed"
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

	// Embeddings powers semantic search. Both are nil-safe: with no provider
	// configured the embed step is skipped and search falls back to keywords.
	Embedder   embed.Provider
	Embeddings *postgres.EmbeddingRepo

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

	// The businesses and their follow-up jobs are committed together, through the
	// outbox. Storing them and *then* enqueueing left a window: a crash in
	// between stranded the businesses in Postgres with no job to ever crawl or
	// score them. Not failed, just invisible.
	//
	// Every business needs scoring. Those with a website get crawled first and
	// the crawl chain ends in a scoring job; the rest are scored immediately.
	queued := 0
	inserted, err := w.deps.Businesses.BulkInsertWithJobs(ctx, batch,
		func(b *domain.Business) (json.RawMessage, bool) {
			next := queue.Job{Type: queue.JobRuleScoring, BusinessID: b.ID}
			if b.Website != "" {
				next = queue.Job{Type: queue.JobWebsiteCrawl, BusinessID: b.ID, URL: b.Website}
			}

			payload, err := json.Marshal(next)
			if err != nil {
				log.Error("could not marshal follow-up job", "business_id", b.ID, "error", err)
				return nil, false
			}
			queued++
			return payload, true
		})
	if err != nil {
		w.failJob(ctx, p.ScrapeJobID, err)
		return fmt.Errorf("store businesses: %w", err)
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

	// Google Maps lets a business put anything in its website field, and small
	// sellers routinely put their Instagram. Crawling that would get us blocked
	// by Meta and we would then report the business's site as "down". Record it
	// as the social profile it is and score it as having no storefront.
	if platform, ok := extract.SocialOnly(job.URL); ok {
		w.log.Info("business has no storefront, only a social profile",
			"business_id", job.BusinessID, "platform", platform, "url", job.URL)

		err := w.deps.Socials.BulkUpsert(ctx, []*domain.SocialProfile{{
			BusinessID: job.BusinessID,
			Platform:   platform,
			URL:        job.URL,
		}})
		if err != nil {
			return fmt.Errorf("store social profile: %w", err)
		}

		return w.deps.Queue.Enqueue(ctx, queue.Job{
			Type: queue.JobRuleScoring, BusinessID: job.BusinessID,
		})
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
		CrawlStatus:      string(res.Status),
	}
	now := time.Now().UTC()
	site.CrawledAt = &now

	if err := w.deps.Websites.Create(ctx, site); err != nil {
		return fmt.Errorf("store website: %w", err)
	}

	// Not being able to read the page is a finding, not a failure. But *why* we
	// could not read it matters enormously: genuinely down is the strongest lead
	// we have, while blocked-by-bot-protection means the site is fine and we
	// simply were not let in.
	if !res.Reachable {
		w.log.Info("no page to assess", "business_id", job.BusinessID, "url", job.URL,
			"status", res.Status, "http", res.StatusCode, "detail", res.Error)

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

	// An Instagram page is not a storefront. Scoring it as a website would rank
	// the best lead we have as a mediocre one.
	if platform, ok := extract.SocialOnly(business.Website); ok {
		evalCtx.SocialOnly = true
		evalCtx.SocialPlatform = platform
		evalCtx.HasWebsite = false
	}

	// A website row exists only if a crawl ran, so its absence is not an error.
	site, err := w.deps.Websites.GetByBusinessID(ctx, job.BusinessID)
	if err == nil && !evalCtx.SocialOnly {
		// The crawl's own verdict, not a guess from the status code: a 403 means
		// the site is up and blocking us, which is not the same as being down.
		evalCtx.IsReachable = site.CrawlStatus == string(crawler.StatusLive)
		evalCtx.SiteDown = site.CrawlStatus == string(crawler.StatusDown)
		evalCtx.SiteOpaque = site.CrawlStatus == string(crawler.StatusBlocked) ||
			site.CrawlStatus == string(crawler.StatusUnknown)
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
		// RankedScore, not Percent: a lead we could not assess has an inflated
		// percentage (few rules applied), and the list is sorted by this.
		TotalScore: result.RankedScore(),
		Priority:   result.Priority(),
		Breakdown:  breakdownToMap(result.Breakdown),
	}

	// An AI audit, when one exists, adjusts the total. A bad site (low quality)
	// means a better lead, so the AI score is inverted before it is folded in.
	if audit, err := w.deps.Audits.GetByBusinessID(ctx, job.BusinessID); err == nil {
		avg := (audit.QualityScore + audit.SEOScore + audit.MobileScore) / 3
		score.AIScore = (10 - avg) * 10
		score.TotalScore = (result.RankedScore()*7 + score.AIScore*3) / 10
	}

	score.SalesSuggestion = result.SalesSuggestion(business.Name, evalCtx)

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

	return w.deps.Queue.Enqueue(ctx, queue.Job{
		Type: queue.JobEmbedLead, BusinessID: job.BusinessID,
	})
}

// embedLead turns the finished lead into a vector so semantic search can find
// it. This runs last, once the business, its crawl, its score and its audit are
// all known — embedding it earlier would capture a half-built lead.
func (w *Worker) embedLead(ctx context.Context, job *queue.Job) error {
	if job.BusinessID == "" {
		return errors.New("embed_lead needs a business_id")
	}

	// No provider is a normal configuration, not a failure: search falls back to
	// keyword matching.
	if !embed.Enabled(w.deps.Embedder) || w.deps.Embeddings == nil {
		return nil
	}

	doc, err := w.buildDocument(ctx, job.BusinessID)
	if err != nil {
		return err
	}

	content := doc.Text()
	hash := embed.Hash(content)
	model := w.deps.Embedder.Model()

	// Re-embedding an unchanged lead is a wasted API call, and this job re-runs
	// on every rescore.
	needed, err := w.deps.Embeddings.NeedsEmbedding(ctx, job.BusinessID, hash, model)
	if err != nil {
		return fmt.Errorf("check embedding: %w", err)
	}
	if !needed {
		w.log.Debug("lead unchanged, skipping embed", "business_id", job.BusinessID)
		return nil
	}

	vecs, err := w.deps.Embedder.EmbedDocuments(ctx, []string{content})
	if err != nil {
		if errors.Is(err, embed.ErrNotConfigured) {
			return nil
		}
		return fmt.Errorf("embed lead: %w", err)
	}
	if len(vecs) != 1 {
		return fmt.Errorf("embedder returned %d vectors for one document", len(vecs))
	}

	if err := w.deps.Embeddings.Upsert(ctx, job.BusinessID, content, hash, model, vecs[0]); err != nil {
		return err
	}

	w.log.Info("lead embedded", "business_id", job.BusinessID, "model", model)
	return nil
}

// buildDocument assembles the text that represents this lead for retrieval.
func (w *Worker) buildDocument(ctx context.Context, businessID string) (embed.LeadDocument, error) {
	business, err := w.deps.Businesses.GetByID(ctx, businessID)
	if err != nil {
		return embed.LeadDocument{}, fmt.Errorf("load business: %w", err)
	}

	doc := embed.LeadDocument{
		Name:     business.Name,
		Category: business.Category,
		Address:  business.Address,
		Rating:   business.Rating,
		Website:  business.Website,
	}

	if _, ok := extract.SocialOnly(business.Website); ok {
		doc.SocialOnly = true
	}

	// Review count lives in metadata; it is the signal that says whether the
	// business is actually trading.
	var meta struct {
		ReviewCount int `json:"review_count"`
	}
	if len(business.Metadata) > 0 {
		_ = json.Unmarshal(business.Metadata, &meta)
		doc.Reviews = meta.ReviewCount
	}

	// The rest is best-effort: a lead with no crawl or no score is still worth
	// embedding on the strength of its Maps data alone.
	if score, err := w.deps.Scores.GetByBusinessID(ctx, businessID); err == nil {
		doc.Priority = score.Priority
		for gap, weight := range score.Breakdown {
			if weight > 0 {
				doc.Gaps = append(doc.Gaps, gap)
			}
		}
		sort.Strings(doc.Gaps) // stable text, so the hash is stable
	}

	if site, err := w.deps.Websites.GetByBusinessID(ctx, businessID); err == nil {
		doc.SiteTitle = site.Title
		if cr, err := w.deps.CrawlResults.GetByWebsiteID(ctx, site.ID); err == nil && cr.HTML != "" {
			doc.SiteText = extract.StripTags(cr.HTML)
		} else if site.MetaDescription != "" {
			doc.SiteText = site.MetaDescription
		}
	}

	if contacts, err := w.deps.Contacts.GetByBusinessID(ctx, businessID); err == nil {
		for _, c := range contacts {
			if c.Email != "" {
				doc.Contacts = append(doc.Contacts, c.Email)
			}
		}
		sort.Strings(doc.Contacts)
	}

	return doc, nil
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
