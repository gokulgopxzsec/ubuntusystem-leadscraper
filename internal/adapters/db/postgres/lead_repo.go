package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/makeforme/leadscraper/internal/domain"
)

// LeadRepo serves the dashboard: the joined business + score view, with the
// filtering, sorting and pagination a person actually needs to work a list.
//
// This replaces ListHighPriority, which hard-filtered to high and medium and so
// made every low-priority lead invisible — you cannot manage rows you cannot see.
type LeadRepo struct {
	pool *pgxpool.Pool
}

func NewLeadRepo(pool *pgxpool.Pool) *LeadRepo {
	return &LeadRepo{pool: pool}
}

// WebPresence is the filter people actually think in: not "has_website", but
// "which of these has nowhere to send a customer".
type WebPresence string

const (
	WebAny        WebPresence = ""
	WebNone       WebPresence = "none"   // no website at all
	WebSocialOnly WebPresence = "social" // an Instagram page, not a storefront
	WebSite       WebPresence = "site"   // a real website
)

type LeadFilter struct {
	Search   string
	Priority string
	Category string
	Source   string
	Web      WebPresence

	MinScore int
	// MinReviews filters out the long tail. A business with two reviews may not
	// really be trading, and is not worth a call.
	MinReviews int
	HasPhone   bool
	HasEmail   bool

	SortBy    string
	SortOrder string
	Page      int
	Limit     int
}

// Lead is one row of the dashboard.
type Lead struct {
	Business *domain.Business  `json:"business"`
	Score    *domain.LeadScore `json:"score,omitempty"`

	// Denormalised for the list view, so rendering a page of leads does not turn
	// into a query per row.
	ContactCount int    `json:"contact_count"`
	SocialCount  int    `json:"social_count"`
	Email        string `json:"email,omitempty"`
	// SiteStatus is the crawl verdict: live | down | blocked | unknown. The UI
	// needs it to tell "their site is down" (a lead) apart from "their site
	// blocked our crawler" (not a lead, and not something to say on a call).
	SiteStatus string `json:"site_status,omitempty"`
	// SiteCode is the raw HTTP status, for the detail view.
	SiteCode int `json:"site_code,omitempty"`
}

func (r *LeadRepo) List(ctx context.Context, f LeadFilter) ([]*Lead, int64, error) {
	var (
		where []string
		args  []any
	)
	add := func(clause string, val any) {
		args = append(args, val)
		where = append(where, fmt.Sprintf(clause, len(args)))
	}

	if f.Search != "" {
		add("(b.name ILIKE $%[1]d OR b.category ILIKE $%[1]d OR b.address ILIKE $%[1]d)", "%"+f.Search+"%")
	}
	if f.Priority != "" {
		add("s.priority = $%d", f.Priority)
	}
	if f.Category != "" {
		add("b.category ILIKE $%d", "%"+f.Category+"%")
	}
	if f.Source != "" {
		add("b.source = $%d", f.Source)
	}
	if f.MinScore > 0 {
		add("s.total_score >= $%d", f.MinScore)
	}
	if f.MinReviews > 0 {
		add("coalesce((b.metadata->>'review_count')::int, 0) >= $%d", f.MinReviews)
	}

	// Web presence reads off the score breakdown rather than the website column,
	// because "has an Instagram page in the website field" is exactly the case
	// the raw column gets wrong.
	switch f.Web {
	case WebNone:
		where = append(where, `(s.breakdown->>'no_website')::int > 0`)
	case WebSocialOnly:
		where = append(where, `(s.breakdown->>'social_only')::int > 0`)
	case WebSite:
		where = append(where, `coalesce((s.breakdown->>'no_website')::int, 0) = 0
		                   AND coalesce((s.breakdown->>'social_only')::int, 0) = 0
		                   AND b.website IS NOT NULL AND b.website <> ''`)
	}

	if f.HasPhone {
		where = append(where, `(b.phone IS NOT NULL AND b.phone <> ''
		                        OR EXISTS (SELECT 1 FROM contacts c
		                                   WHERE c.business_id = b.id
		                                     AND coalesce(c.phone, c.whatsapp, '') <> ''))`)
	}
	if f.HasEmail {
		where = append(where, `EXISTS (SELECT 1 FROM contacts c
		                               WHERE c.business_id = b.id AND coalesce(c.email,'') <> '')`)
	}

	clause := ""
	if len(where) > 0 {
		clause = " WHERE " + strings.Join(where, " AND ")
	}

	// A lead with no score row has not finished the pipeline yet. It still shows,
	// so a scrape in progress does not look like an empty database.
	from := `
		FROM businesses b
		LEFT JOIN lead_scores s ON s.business_id = b.id`

	var total int64
	if err := r.pool.QueryRow(ctx, `SELECT count(*)`+from+clause, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count leads: %w", err)
	}

	limit, offset := paginate(f.Page, f.Limit)
	args = append(args, limit, offset)

	query := fmt.Sprintf(`
		SELECT b.id, b.name, b.address, b.phone, b.rating, b.website, b.category,
		       b.source, b.metadata, b.created_at, b.updated_at,
		       s.total_score, s.rule_score, s.ai_score, s.priority, s.breakdown,
		       s.sales_suggestion, s.scored_at,
		       (SELECT count(*) FROM contacts c WHERE c.business_id = b.id),
		       (SELECT count(*) FROM social_profiles sp WHERE sp.business_id = b.id),
		       (SELECT c.email FROM contacts c
		         WHERE c.business_id = b.id AND coalesce(c.email,'') <> ''
		         ORDER BY c.confidence DESC LIMIT 1),
		       (SELECT w.status_code FROM websites w WHERE w.business_id = b.id),
		       (SELECT w.crawl_status FROM websites w WHERE w.business_id = b.id)
		%s%s
		ORDER BY %s
		LIMIT $%d OFFSET $%d`,
		from, clause, leadSort(f.SortBy, f.SortOrder), len(args)-1, len(args))

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("list leads: %w", err)
	}
	defer rows.Close()

	var out []*Lead
	for rows.Next() {
		var (
			b                 domain.Business
			addr, phone       *string
			website, category *string
			rating            *float64
			metadata          []byte

			totalScore, ruleScore, aiScore *int32
			priority                       *string
			breakdown                      []byte
			suggestion                     *string
			scoredAt                       *interface{}

			contactCount, socialCount int
			email                     *string
			siteCode                  *int32
			siteStatus                *string
		)

		err := rows.Scan(&b.ID, &b.Name, &addr, &phone, &rating, &website, &category,
			&b.Source, &metadata, &b.CreatedAt, &b.UpdatedAt,
			&totalScore, &ruleScore, &aiScore, &priority, &breakdown, &suggestion, &scoredAt,
			&contactCount, &socialCount, &email, &siteCode, &siteStatus)
		if err != nil {
			return nil, 0, fmt.Errorf("scan lead: %w", err)
		}

		b.Address, b.Phone = str(addr), str(phone)
		b.Website, b.Category = str(website), str(category)
		b.Rating = f64(rating)
		b.Metadata = metadata

		lead := &Lead{
			Business:     &b,
			ContactCount: contactCount,
			SocialCount:  socialCount,
			Email:        str(email),
			SiteCode:     i32(siteCode),
			SiteStatus:   str(siteStatus),
		}

		if priority != nil {
			score := &domain.LeadScore{
				BusinessID:      b.ID,
				TotalScore:      i32(totalScore),
				RuleScore:       i32(ruleScore),
				AIScore:         i32(aiScore),
				Priority:        *priority,
				SalesSuggestion: str(suggestion),
			}
			if len(breakdown) > 0 {
				if err := json.Unmarshal(breakdown, &score.Breakdown); err != nil {
					return nil, 0, fmt.Errorf("unmarshal breakdown: %w", err)
				}
			}
			lead.Score = score
		}

		out = append(out, lead)
	}
	return out, total, rows.Err()
}

// Stats powers the dashboard counters, in one query rather than five.
func (r *LeadRepo) Stats(ctx context.Context) (map[string]int, error) {
	var total, high, medium, low, noWebsite, socialOnly, unscored int

	err := r.pool.QueryRow(ctx, `
		SELECT
			count(*),
			count(*) FILTER (WHERE s.priority = 'high'),
			count(*) FILTER (WHERE s.priority = 'medium'),
			count(*) FILTER (WHERE s.priority = 'low'),
			count(*) FILTER (WHERE (s.breakdown->>'no_website')::int > 0),
			count(*) FILTER (WHERE (s.breakdown->>'social_only')::int > 0),
			count(*) FILTER (WHERE s.business_id IS NULL)
		FROM businesses b
		LEFT JOIN lead_scores s ON s.business_id = b.id`,
	).Scan(&total, &high, &medium, &low, &noWebsite, &socialOnly, &unscored)
	if err != nil {
		return nil, fmt.Errorf("lead stats: %w", err)
	}

	return map[string]int{
		"total":       total,
		"high":        high,
		"medium":      medium,
		"low":         low,
		"no_website":  noWebsite,
		"social_only": socialOnly,
		"unscored":    unscored,
	}, nil
}

// BulkDelete removes several leads at once and reports how many went.
func (r *LeadRepo) BulkDelete(ctx context.Context, ids []string) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	// Every child table cascades from businesses, so one delete is enough.
	tag, err := r.pool.Exec(ctx, `DELETE FROM businesses WHERE id = ANY($1)`, ids)
	if err != nil {
		return 0, fmt.Errorf("bulk delete: %w", err)
	}
	return tag.RowsAffected(), nil
}

// Categories and Sources feed the filter dropdowns, so they only ever offer
// values that actually exist in the data.
func (r *LeadRepo) Categories(ctx context.Context) ([]string, error) {
	return r.distinct(ctx, `
		SELECT DISTINCT category FROM businesses
		WHERE category IS NOT NULL AND category <> ''
		ORDER BY category`)
}

func (r *LeadRepo) Sources(ctx context.Context) ([]string, error) {
	return r.distinct(ctx, `SELECT DISTINCT source FROM businesses ORDER BY source`)
}

func (r *LeadRepo) distinct(ctx context.Context, query string) ([]string, error) {
	rows, err := r.pool.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("distinct values: %w", err)
	}
	defer rows.Close()

	out := []string{}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// leadSort maps user input onto a fixed set of columns. The sort column cannot
// be a bind parameter, so it must never be interpolated from raw input.
func leadSort(sortBy, order string) string {
	col, ok := map[string]string{
		"score":      "s.total_score",
		"name":       "b.name",
		"rating":     "b.rating",
		"reviews":    "(b.metadata->>'review_count')::int",
		"category":   "b.category",
		"created_at": "b.created_at",
		"updated_at": "b.updated_at",
	}[sortBy]
	if !ok {
		col = "s.total_score"
	}

	dir := "DESC"
	if strings.EqualFold(order, "asc") {
		dir = "ASC"
	}

	// Unscored leads sort last whichever way you order, rather than jumping to the
	// top of a descending list as NULLs otherwise would.
	return fmt.Sprintf("%s %s NULLS LAST, b.name ASC", col, dir)
}
