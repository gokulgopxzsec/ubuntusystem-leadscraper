package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/makeforme/leadscraper/internal/domain"
)

// ---------- websites ----------

type WebsiteRepo struct{ pool *pgxpool.Pool }

func NewWebsiteRepo(pool *pgxpool.Pool) *WebsiteRepo { return &WebsiteRepo{pool: pool} }

func (r *WebsiteRepo) Create(ctx context.Context, w *domain.Website) error {
	err := r.pool.QueryRow(ctx, `
		INSERT INTO websites
			(business_id, url, status_code, load_time_ms, has_ssl, has_booking,
			 is_mobile_friendly, pages_crawled, title, meta_description, crawled_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
		RETURNING id, created_at`,
		w.BusinessID, w.URL, w.StatusCode, w.LoadTimeMs, w.HasSSL, w.HasBooking,
		w.IsMobileFriendly, w.PagesCrawled, nullStr(w.Title),
		nullStr(w.MetaDescription), w.CrawledAt,
	).Scan(&w.ID, &w.CreatedAt)
	if err != nil {
		return fmt.Errorf("insert website: %w", err)
	}
	return nil
}

func (r *WebsiteRepo) GetByBusinessID(ctx context.Context, businessID string) (*domain.Website, error) {
	var (
		w                 domain.Website
		title, metaDesc   *string
		statusCode, loadMs *int32
	)
	err := r.pool.QueryRow(ctx, `
		SELECT id, business_id, url, status_code, load_time_ms, has_ssl, has_booking,
		       is_mobile_friendly, pages_crawled, title, meta_description, crawled_at, created_at
		FROM websites WHERE business_id = $1
		ORDER BY created_at DESC LIMIT 1`, businessID,
	).Scan(&w.ID, &w.BusinessID, &w.URL, &statusCode, &loadMs, &w.HasSSL,
		&w.HasBooking, &w.IsMobileFriendly, &w.PagesCrawled, &title, &metaDesc,
		&w.CrawledAt, &w.CreatedAt)
	if err != nil {
		return nil, mapNoRows(fmt.Errorf("get website: %w", err))
	}

	w.StatusCode = i32(statusCode)
	w.LoadTimeMs = i32(loadMs)
	w.Title = str(title)
	w.MetaDescription = str(metaDesc)
	return &w, nil
}

func (r *WebsiteRepo) Update(ctx context.Context, w *domain.Website) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE websites SET
			url = $2, status_code = $3, load_time_ms = $4, has_ssl = $5,
			has_booking = $6, is_mobile_friendly = $7, pages_crawled = $8,
			title = $9, meta_description = $10, crawled_at = $11
		WHERE id = $1`,
		w.ID, w.URL, w.StatusCode, w.LoadTimeMs, w.HasSSL, w.HasBooking,
		w.IsMobileFriendly, w.PagesCrawled, nullStr(w.Title),
		nullStr(w.MetaDescription), w.CrawledAt)
	if err != nil {
		return fmt.Errorf("update website: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ---------- contacts ----------

type ContactRepo struct{ pool *pgxpool.Pool }

func NewContactRepo(pool *pgxpool.Pool) *ContactRepo { return &ContactRepo{pool: pool} }

func (r *ContactRepo) Create(ctx context.Context, c *domain.Contact) error {
	err := r.pool.QueryRow(ctx, `
		INSERT INTO contacts (business_id, email, phone, whatsapp, contact_type, source, confidence)
		VALUES ($1,$2,$3,$4,$5,$6,$7)
		RETURNING id, created_at`,
		c.BusinessID, nullStr(c.Email), nullStr(c.Phone), nullStr(c.WhatsApp),
		c.ContactType, nullStr(c.Source), c.Confidence,
	).Scan(&c.ID, &c.CreatedAt)
	if err != nil {
		return fmt.Errorf("insert contact: %w", err)
	}
	return nil
}

func (r *ContactRepo) GetByBusinessID(ctx context.Context, businessID string) ([]*domain.Contact, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, business_id, email, phone, whatsapp, contact_type, source, confidence, created_at
		FROM contacts WHERE business_id = $1 ORDER BY confidence DESC, created_at`, businessID)
	if err != nil {
		return nil, fmt.Errorf("get contacts: %w", err)
	}
	defer rows.Close()

	var out []*domain.Contact
	for rows.Next() {
		var (
			c                      domain.Contact
			email, phone, whatsapp *string
			source                 *string
		)
		if err := rows.Scan(&c.ID, &c.BusinessID, &email, &phone, &whatsapp,
			&c.ContactType, &source, &c.Confidence, &c.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan contact: %w", err)
		}
		c.Email, c.Phone, c.WhatsApp, c.Source = str(email), str(phone), str(whatsapp), str(source)
		out = append(out, &c)
	}
	return out, rows.Err()
}

// BulkUpsert skips contacts already recorded for the business. The schema has
// no unique constraint here (a business legitimately has several emails), so
// dedup is an explicit NOT EXISTS rather than ON CONFLICT.
func (r *ContactRepo) BulkUpsert(ctx context.Context, contacts []*domain.Contact) error {
	if len(contacts) == 0 {
		return nil
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin contact upsert: %w", err)
	}
	defer tx.Rollback(ctx)

	for _, c := range contacts {
		_, err := tx.Exec(ctx, `
			INSERT INTO contacts (business_id, email, phone, whatsapp, contact_type, source, confidence)
			SELECT $1,$2,$3,$4,$5,$6,$7
			WHERE NOT EXISTS (
				SELECT 1 FROM contacts
				WHERE business_id = $1
				  AND coalesce(email,'')    = coalesce($2::text,'')
				  AND coalesce(phone,'')    = coalesce($3::text,'')
				  AND coalesce(whatsapp,'') = coalesce($4::text,'')
			)`,
			c.BusinessID, nullStr(c.Email), nullStr(c.Phone), nullStr(c.WhatsApp),
			c.ContactType, nullStr(c.Source), c.Confidence)
		if err != nil {
			return fmt.Errorf("upsert contact: %w", err)
		}
	}
	return tx.Commit(ctx)
}

// ---------- social profiles ----------

type SocialRepo struct{ pool *pgxpool.Pool }

func NewSocialRepo(pool *pgxpool.Pool) *SocialRepo { return &SocialRepo{pool: pool} }

func (r *SocialRepo) Create(ctx context.Context, p *domain.SocialProfile) error {
	err := r.pool.QueryRow(ctx, `
		INSERT INTO social_profiles (business_id, platform, url, followers, verified)
		VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (business_id, platform) DO UPDATE SET
			url = EXCLUDED.url, followers = EXCLUDED.followers, verified = EXCLUDED.verified
		RETURNING id, created_at`,
		p.BusinessID, p.Platform, p.URL, p.Followers, p.Verified,
	).Scan(&p.ID, &p.CreatedAt)
	if err != nil {
		return fmt.Errorf("insert social profile: %w", err)
	}
	return nil
}

func (r *SocialRepo) GetByBusinessID(ctx context.Context, businessID string) ([]*domain.SocialProfile, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, business_id, platform, url, followers, verified, created_at
		FROM social_profiles WHERE business_id = $1 ORDER BY platform`, businessID)
	if err != nil {
		return nil, fmt.Errorf("get social profiles: %w", err)
	}
	defer rows.Close()

	var out []*domain.SocialProfile
	for rows.Next() {
		var p domain.SocialProfile
		if err := rows.Scan(&p.ID, &p.BusinessID, &p.Platform, &p.URL,
			&p.Followers, &p.Verified, &p.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan social profile: %w", err)
		}
		out = append(out, &p)
	}
	return out, rows.Err()
}

func (r *SocialRepo) BulkUpsert(ctx context.Context, profiles []*domain.SocialProfile) error {
	if len(profiles) == 0 {
		return nil
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin social upsert: %w", err)
	}
	defer tx.Rollback(ctx)

	for _, p := range profiles {
		_, err := tx.Exec(ctx, `
			INSERT INTO social_profiles (business_id, platform, url, followers, verified)
			VALUES ($1,$2,$3,$4,$5)
			ON CONFLICT (business_id, platform) DO UPDATE SET
				url = EXCLUDED.url,
				followers = GREATEST(EXCLUDED.followers, social_profiles.followers),
				verified = EXCLUDED.verified OR social_profiles.verified`,
			p.BusinessID, p.Platform, p.URL, p.Followers, p.Verified)
		if err != nil {
			return fmt.Errorf("upsert social profile: %w", err)
		}
	}
	return tx.Commit(ctx)
}

// ---------- audit reports ----------

type AuditRepo struct{ pool *pgxpool.Pool }

func NewAuditRepo(pool *pgxpool.Pool) *AuditRepo { return &AuditRepo{pool: pool} }

func (r *AuditRepo) Create(ctx context.Context, a *domain.AuditReport) error {
	err := r.pool.QueryRow(ctx, `
		INSERT INTO audit_reports
			(website_id, business_id, quality_score, seo_score, mobile_score,
			 issues, recommendations, summary, services_to_offer, audited_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9, coalesce($10, now()))
		RETURNING id, audited_at, created_at`,
		nullStr(a.WebsiteID), a.BusinessID, a.QualityScore, a.SEOScore, a.MobileScore,
		a.Issues, a.Recommendations, nullStr(a.Summary), a.ServicesToOffer,
		nullTime(a.AuditedAt),
	).Scan(&a.ID, &a.AuditedAt, &a.CreatedAt)
	if err != nil {
		return fmt.Errorf("insert audit report: %w", err)
	}
	return nil
}

func (r *AuditRepo) GetByBusinessID(ctx context.Context, businessID string) (*domain.AuditReport, error) {
	var (
		a         domain.AuditReport
		websiteID *string
		summary   *string
	)
	err := r.pool.QueryRow(ctx, `
		SELECT id, website_id, business_id, quality_score, seo_score, mobile_score,
		       issues, recommendations, summary, services_to_offer, audited_at, created_at
		FROM audit_reports WHERE business_id = $1
		ORDER BY audited_at DESC LIMIT 1`, businessID,
	).Scan(&a.ID, &websiteID, &a.BusinessID, &a.QualityScore, &a.SEOScore, &a.MobileScore,
		&a.Issues, &a.Recommendations, &summary, &a.ServicesToOffer, &a.AuditedAt, &a.CreatedAt)
	if err != nil {
		return nil, mapNoRows(fmt.Errorf("get audit report: %w", err))
	}

	a.WebsiteID = str(websiteID)
	a.Summary = str(summary)
	return &a, nil
}

// ---------- lead scores ----------

type ScoreRepo struct{ pool *pgxpool.Pool }

func NewScoreRepo(pool *pgxpool.Pool) *ScoreRepo { return &ScoreRepo{pool: pool} }

// Create upserts: lead_scores has UNIQUE(business_id), and rescoring a lead is
// the normal case, not an error.
func (r *ScoreRepo) Create(ctx context.Context, s *domain.LeadScore) error {
	breakdown, err := json.Marshal(s.Breakdown)
	if err != nil {
		return fmt.Errorf("marshal breakdown: %w", err)
	}

	err = r.pool.QueryRow(ctx, `
		INSERT INTO lead_scores
			(business_id, total_score, rule_score, ai_score, priority, breakdown, sales_suggestion)
		VALUES ($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT (business_id) DO UPDATE SET
			total_score = EXCLUDED.total_score,
			rule_score = EXCLUDED.rule_score,
			ai_score = EXCLUDED.ai_score,
			priority = EXCLUDED.priority,
			breakdown = EXCLUDED.breakdown,
			sales_suggestion = COALESCE(EXCLUDED.sales_suggestion, lead_scores.sales_suggestion),
			scored_at = now()
		RETURNING id, scored_at, created_at`,
		s.BusinessID, s.TotalScore, s.RuleScore, s.AIScore, s.Priority,
		breakdown, nullStr(s.SalesSuggestion),
	).Scan(&s.ID, &s.ScoredAt, &s.CreatedAt)
	if err != nil {
		return fmt.Errorf("upsert lead score: %w", err)
	}
	return nil
}

func (r *ScoreRepo) Update(ctx context.Context, s *domain.LeadScore) error {
	return r.Create(ctx, s)
}

func (r *ScoreRepo) GetByBusinessID(ctx context.Context, businessID string) (*domain.LeadScore, error) {
	var (
		s          domain.LeadScore
		breakdown  []byte
		suggestion *string
	)
	err := r.pool.QueryRow(ctx, `
		SELECT id, business_id, total_score, rule_score, ai_score, priority,
		       breakdown, sales_suggestion, scored_at, created_at
		FROM lead_scores WHERE business_id = $1`, businessID,
	).Scan(&s.ID, &s.BusinessID, &s.TotalScore, &s.RuleScore, &s.AIScore,
		&s.Priority, &breakdown, &suggestion, &s.ScoredAt, &s.CreatedAt)
	if err != nil {
		return nil, mapNoRows(fmt.Errorf("get lead score: %w", err))
	}

	s.SalesSuggestion = str(suggestion)
	if len(breakdown) > 0 {
		if err := json.Unmarshal(breakdown, &s.Breakdown); err != nil {
			return nil, fmt.Errorf("unmarshal breakdown: %w", err)
		}
	}
	return &s, nil
}

func (r *ScoreRepo) ListHighPriority(ctx context.Context, limit int) ([]*domain.LeadScore, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}

	rows, err := r.pool.Query(ctx, `
		SELECT id, business_id, total_score, rule_score, ai_score, priority,
		       breakdown, sales_suggestion, scored_at, created_at
		FROM lead_scores
		WHERE priority IN ('high', 'medium')
		ORDER BY total_score DESC, scored_at DESC
		LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("list high priority: %w", err)
	}
	defer rows.Close()

	var out []*domain.LeadScore
	for rows.Next() {
		var (
			s          domain.LeadScore
			breakdown  []byte
			suggestion *string
		)
		if err := rows.Scan(&s.ID, &s.BusinessID, &s.TotalScore, &s.RuleScore,
			&s.AIScore, &s.Priority, &breakdown, &suggestion, &s.ScoredAt, &s.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan lead score: %w", err)
		}
		s.SalesSuggestion = str(suggestion)
		if len(breakdown) > 0 {
			if err := json.Unmarshal(breakdown, &s.Breakdown); err != nil {
				return nil, fmt.Errorf("unmarshal breakdown: %w", err)
			}
		}
		out = append(out, &s)
	}
	return out, rows.Err()
}

// ---------- scrape jobs ----------

type JobRepo struct{ pool *pgxpool.Pool }

func NewJobRepo(pool *pgxpool.Pool) *JobRepo { return &JobRepo{pool: pool} }

func (r *JobRepo) Create(ctx context.Context, j *domain.ScrapeJob) error {
	if j.Status == "" {
		j.Status = "pending"
	}
	err := r.pool.QueryRow(ctx, `
		INSERT INTO scrape_jobs (source, category, location, status, params)
		VALUES ($1,$2,$3,$4,$5)
		RETURNING id, created_at`,
		j.Source, nullStr(j.Category), nullStr(j.Location), j.Status,
		jsonOrEmpty(j.Params),
	).Scan(&j.ID, &j.CreatedAt)
	if err != nil {
		return fmt.Errorf("insert scrape job: %w", err)
	}
	return nil
}

func (r *JobRepo) Update(ctx context.Context, j *domain.ScrapeJob) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE scrape_jobs SET
			status = $2, total_found = $3, success_count = $4, fail_count = $5,
			started_at = $6, completed_at = $7, error = $8
		WHERE id = $1`,
		j.ID, j.Status, j.TotalFound, j.SuccessCount, j.FailCount,
		j.StartedAt, j.CompletedAt, nullStr(j.Error))
	if err != nil {
		return fmt.Errorf("update scrape job: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *JobRepo) GetByID(ctx context.Context, id string) (*domain.ScrapeJob, error) {
	var (
		j                          domain.ScrapeJob
		category, location, errMsg *string
		params                     []byte
	)
	err := r.pool.QueryRow(ctx, `
		SELECT id, source, category, location, status, total_found, success_count,
		       fail_count, params, started_at, completed_at, error, created_at
		FROM scrape_jobs WHERE id = $1`, id,
	).Scan(&j.ID, &j.Source, &category, &location, &j.Status, &j.TotalFound,
		&j.SuccessCount, &j.FailCount, &params, &j.StartedAt, &j.CompletedAt,
		&errMsg, &j.CreatedAt)
	if err != nil {
		return nil, mapNoRows(fmt.Errorf("get scrape job: %w", err))
	}

	j.Category, j.Location, j.Error = str(category), str(location), str(errMsg)
	j.Params = params
	return &j, nil
}

func (r *JobRepo) List(ctx context.Context, f domain.JobFilter) ([]*domain.ScrapeJob, int64, error) {
	var (
		where []string
		args  []any
	)
	add := func(clause string, val any) {
		args = append(args, val)
		where = append(where, fmt.Sprintf(clause, len(args)))
	}

	if f.Source != "" {
		add("source = $%d", f.Source)
	}
	if f.Category != "" {
		add("category = $%d", f.Category)
	}
	if f.Status != "" {
		add("status = $%d", f.Status)
	}

	clause := ""
	if len(where) > 0 {
		clause = " WHERE " + strings.Join(where, " AND ")
	}

	var total int64
	if err := r.pool.QueryRow(ctx, `SELECT count(*) FROM scrape_jobs`+clause, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count scrape jobs: %w", err)
	}

	limit, offset := paginate(f.Page, f.Limit)
	args = append(args, limit, offset)

	rows, err := r.pool.Query(ctx, fmt.Sprintf(`
		SELECT id, source, category, location, status, total_found, success_count,
		       fail_count, params, started_at, completed_at, error, created_at
		FROM scrape_jobs%s ORDER BY created_at DESC LIMIT $%d OFFSET $%d`,
		clause, len(args)-1, len(args)), args...)
	if err != nil {
		return nil, 0, fmt.Errorf("list scrape jobs: %w", err)
	}
	defer rows.Close()

	var out []*domain.ScrapeJob
	for rows.Next() {
		var (
			j                          domain.ScrapeJob
			category, location, errMsg *string
			params                     []byte
		)
		if err := rows.Scan(&j.ID, &j.Source, &category, &location, &j.Status,
			&j.TotalFound, &j.SuccessCount, &j.FailCount, &params, &j.StartedAt,
			&j.CompletedAt, &errMsg, &j.CreatedAt); err != nil {
			return nil, 0, fmt.Errorf("scan scrape job: %w", err)
		}
		j.Category, j.Location, j.Error = str(category), str(location), str(errMsg)
		j.Params = params
		out = append(out, &j)
	}
	return out, total, rows.Err()
}

// ---------- sources ----------

type SourceRepo struct{ pool *pgxpool.Pool }

func NewSourceRepo(pool *pgxpool.Pool) *SourceRepo { return &SourceRepo{pool: pool} }

func (r *SourceRepo) Create(ctx context.Context, s *domain.Source) error {
	if s.Type == "" {
		s.Type = "scraper"
	}
	err := r.pool.QueryRow(ctx, `
		INSERT INTO sources (name, type, enabled, config)
		VALUES ($1,$2,$3,$4)
		ON CONFLICT (name) DO UPDATE SET
			type = EXCLUDED.type, enabled = EXCLUDED.enabled,
			config = EXCLUDED.config, updated_at = now()
		RETURNING id, created_at, updated_at`,
		s.Name, s.Type, s.Enabled, jsonOrEmpty(s.Config),
	).Scan(&s.ID, &s.CreatedAt, &s.UpdatedAt)
	if err != nil {
		return fmt.Errorf("insert source: %w", err)
	}
	return nil
}

func (r *SourceRepo) GetByName(ctx context.Context, name string) (*domain.Source, error) {
	var (
		s      domain.Source
		config []byte
	)
	err := r.pool.QueryRow(ctx, `
		SELECT id, name, type, enabled, config, last_run_at, created_at, updated_at
		FROM sources WHERE name = $1`, name,
	).Scan(&s.ID, &s.Name, &s.Type, &s.Enabled, &config, &s.LastRunAt,
		&s.CreatedAt, &s.UpdatedAt)
	if err != nil {
		return nil, mapNoRows(fmt.Errorf("get source: %w", err))
	}
	s.Config = config
	return &s, nil
}

func (r *SourceRepo) List(ctx context.Context) ([]*domain.Source, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, name, type, enabled, config, last_run_at, created_at, updated_at
		FROM sources ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list sources: %w", err)
	}
	defer rows.Close()

	var out []*domain.Source
	for rows.Next() {
		var (
			s      domain.Source
			config []byte
		)
		if err := rows.Scan(&s.ID, &s.Name, &s.Type, &s.Enabled, &config,
			&s.LastRunAt, &s.CreatedAt, &s.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan source: %w", err)
		}
		s.Config = config
		out = append(out, &s)
	}
	return out, rows.Err()
}

func (r *SourceRepo) Update(ctx context.Context, s *domain.Source) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE sources SET type = $2, enabled = $3, config = $4,
			last_run_at = $5, updated_at = now()
		WHERE id = $1`,
		s.ID, s.Type, s.Enabled, jsonOrEmpty(s.Config), s.LastRunAt)
	if err != nil {
		return fmt.Errorf("update source: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ---------- crawl results ----------

type CrawlResultRepo struct{ pool *pgxpool.Pool }

func NewCrawlResultRepo(pool *pgxpool.Pool) *CrawlResultRepo {
	return &CrawlResultRepo{pool: pool}
}

func (r *CrawlResultRepo) Create(ctx context.Context, c *domain.CrawlResult) error {
	metaTags, err := json.Marshal(c.MetaTags)
	if err != nil {
		return fmt.Errorf("marshal meta tags: %w", err)
	}
	if c.Links == nil {
		c.Links = []string{}
	}

	err = r.pool.QueryRow(ctx, `
		INSERT INTO crawl_results (website_id, url, status_code, html, title, meta_tags, links, error)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		RETURNING id, crawled_at`,
		c.WebsiteID, c.URL, c.StatusCode, nullStr(c.HTML), nullStr(c.Title),
		metaTags, c.Links, nullStr(c.Error),
	).Scan(&c.ID, &c.CrawledAt)
	if err != nil {
		return fmt.Errorf("insert crawl result: %w", err)
	}
	return nil
}

func (r *CrawlResultRepo) GetByWebsiteID(ctx context.Context, websiteID string) (*domain.CrawlResult, error) {
	var (
		c                  domain.CrawlResult
		html, title, cErr  *string
		metaTags           []byte
	)
	err := r.pool.QueryRow(ctx, `
		SELECT id, website_id, url, status_code, html, title, meta_tags, links, error, crawled_at
		FROM crawl_results WHERE website_id = $1
		ORDER BY crawled_at DESC LIMIT 1`, websiteID,
	).Scan(&c.ID, &c.WebsiteID, &c.URL, &c.StatusCode, &html, &title,
		&metaTags, &c.Links, &cErr, &c.CrawledAt)
	if err != nil {
		return nil, mapNoRows(fmt.Errorf("get crawl result: %w", err))
	}

	c.HTML, c.Title, c.Error = str(html), str(title), str(cErr)
	if len(metaTags) > 0 {
		if err := json.Unmarshal(metaTags, &c.MetaTags); err != nil {
			return nil, fmt.Errorf("unmarshal meta tags: %w", err)
		}
	}
	return &c, nil
}

// ---------- technologies ----------

type TechnologyRepo struct{ pool *pgxpool.Pool }

func NewTechnologyRepo(pool *pgxpool.Pool) *TechnologyRepo {
	return &TechnologyRepo{pool: pool}
}

func (r *TechnologyRepo) CreateForWebsite(ctx context.Context, websiteID string, t *domain.Technology) error {
	if t.Category == "" {
		t.Category = "unknown"
	}
	_, err := r.pool.Exec(ctx, `
		INSERT INTO technologies (website_id, name, version, category)
		VALUES ($1,$2,$3,$4)`,
		websiteID, t.Name, nullStr(t.Version), t.Category)
	if err != nil {
		return fmt.Errorf("insert technology: %w", err)
	}
	return nil
}

// ReplaceForWebsite swaps the whole technology set for a website atomically, so
// a re-crawl never leaves stale detections behind or a half-empty window.
func (r *TechnologyRepo) ReplaceForWebsite(ctx context.Context, websiteID string, techs []domain.Technology) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin technology replace: %w", err)
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `DELETE FROM technologies WHERE website_id = $1`, websiteID); err != nil {
		return fmt.Errorf("delete technologies: %w", err)
	}

	for _, t := range techs {
		if t.Category == "" {
			t.Category = "unknown"
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO technologies (website_id, name, version, category)
			VALUES ($1,$2,$3,$4)`,
			websiteID, t.Name, nullStr(t.Version), t.Category); err != nil {
			return fmt.Errorf("insert technology %q: %w", t.Name, err)
		}
	}
	return tx.Commit(ctx)
}

func (r *TechnologyRepo) GetByWebsiteID(ctx context.Context, websiteID string) ([]*domain.Technology, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT name, version, category FROM technologies
		WHERE website_id = $1 ORDER BY category, name`, websiteID)
	if err != nil {
		return nil, fmt.Errorf("get technologies: %w", err)
	}
	defer rows.Close()

	var out []*domain.Technology
	for rows.Next() {
		var (
			t       domain.Technology
			version *string
		)
		if err := rows.Scan(&t.Name, &version, &t.Category); err != nil {
			return nil, fmt.Errorf("scan technology: %w", err)
		}
		t.Version = str(version)
		out = append(out, &t)
	}
	return out, rows.Err()
}

func (r *TechnologyRepo) DeleteByWebsiteID(ctx context.Context, websiteID string) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM technologies WHERE website_id = $1`, websiteID)
	if err != nil {
		return fmt.Errorf("delete technologies: %w", err)
	}
	return nil
}
