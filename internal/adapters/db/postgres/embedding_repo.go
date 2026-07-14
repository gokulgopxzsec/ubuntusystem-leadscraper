package postgres

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/makeforme/leadscraper/internal/domain"
)

type EmbeddingRepo struct {
	pool *pgxpool.Pool
}

func NewEmbeddingRepo(pool *pgxpool.Pool) *EmbeddingRepo {
	return &EmbeddingRepo{pool: pool}
}

// Upsert stores a lead's vector. One row per business, replaced on re-embed.
func (r *EmbeddingRepo) Upsert(ctx context.Context, businessID, content, hash, model string, vec []float32) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO lead_embeddings (business_id, content, embedding, model, content_hash)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (business_id) DO UPDATE SET
			content = EXCLUDED.content,
			embedding = EXCLUDED.embedding,
			model = EXCLUDED.model,
			content_hash = EXCLUDED.content_hash,
			updated_at = now()`,
		businessID, content, vector(vec), model, hash)
	if err != nil {
		return fmt.Errorf("upsert embedding: %w", err)
	}
	return nil
}

// NeedsEmbedding reports whether this lead's text has changed since it was last
// embedded. Re-embedding an unchanged lead is a wasted API call, and across a few
// thousand leads that is the difference between a free tier and a bill.
func (r *EmbeddingRepo) NeedsEmbedding(ctx context.Context, businessID, hash, model string) (bool, error) {
	var storedHash, storedModel string

	err := r.pool.QueryRow(ctx,
		`SELECT content_hash, model FROM lead_embeddings WHERE business_id = $1`,
		businessID).Scan(&storedHash, &storedModel)

	if err != nil {
		if err := mapNoRows(err); err == ErrNotFound {
			return true, nil
		}
		return false, fmt.Errorf("check embedding: %w", err)
	}

	// A different model means a different vector space, and vectors from two
	// spaces cannot be compared. Switching models must re-embed everything.
	return storedHash != hash || storedModel != model, nil
}

// SearchHit is one semantic match.
type SearchHit struct {
	Business   *domain.Business  `json:"business"`
	Score      *domain.LeadScore `json:"score,omitempty"`
	Similarity float64           `json:"similarity"`
	Snippet    string            `json:"snippet,omitempty"`
}

// SearchFilter narrows a semantic search with ordinary SQL predicates. Vector
// search alone cannot express "only high priority": that is a fact, not a
// similarity, and asking the embedding to carry it would be unreliable.
type SearchFilter struct {
	Priority string
	Category string
	Source   string
	Limit    int
	// MinSimilarity drops weak matches. Cosine similarity always returns
	// *something*, so without a floor a query for "dentists" happily returns
	// bakeries, ranked.
	MinSimilarity float64
}

// SemanticSearch ranks leads by cosine similarity to the query vector.
//
// `<=>` is pgvector's cosine *distance* (0 = identical, 2 = opposite), so
// similarity is 1 - distance. Getting that inversion wrong would rank the least
// relevant leads first, and would look plausible.
func (r *EmbeddingRepo) SemanticSearch(ctx context.Context, queryVec []float32, f SearchFilter) ([]SearchHit, error) {
	limit := f.Limit
	if limit <= 0 || limit > 100 {
		limit = 20
	}

	args := []any{vector(queryVec)}
	var where []string

	add := func(clause string, val any) {
		args = append(args, val)
		where = append(where, fmt.Sprintf(clause, len(args)))
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

	clause := ""
	if len(where) > 0 {
		clause = " AND " + strings.Join(where, " AND ")
	}

	args = append(args, f.MinSimilarity, limit)
	minSimIdx, limitIdx := len(args)-1, len(args)

	query := fmt.Sprintf(`
		SELECT b.id, b.name, b.address, b.phone, b.rating, b.website, b.category,
		       b.source, b.metadata, b.created_at, b.updated_at,
		       s.total_score, s.priority, s.sales_suggestion,
		       1 - (e.embedding <=> $1) AS similarity,
		       left(e.content, 240) AS snippet
		FROM lead_embeddings e
		JOIN businesses b ON b.id = e.business_id
		LEFT JOIN lead_scores s ON s.business_id = b.id
		WHERE 1 - (e.embedding <=> $1) >= $%d%s
		ORDER BY e.embedding <=> $1
		LIMIT $%d`, minSimIdx, clause, limitIdx)

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("semantic search: %w", err)
	}
	defer rows.Close()

	var hits []SearchHit
	for rows.Next() {
		var (
			b                 domain.Business
			addr, phone       *string
			website, category *string
			rating            *float64
			metadata          []byte
			totalScore        *int32
			priority          *string
			suggestion        *string
			similarity        float64
			snippet           string
		)

		err := rows.Scan(&b.ID, &b.Name, &addr, &phone, &rating, &website, &category,
			&b.Source, &metadata, &b.CreatedAt, &b.UpdatedAt,
			&totalScore, &priority, &suggestion, &similarity, &snippet)
		if err != nil {
			return nil, fmt.Errorf("scan search hit: %w", err)
		}

		b.Address, b.Phone = str(addr), str(phone)
		b.Website, b.Category = str(website), str(category)
		b.Rating = f64(rating)
		b.Metadata = metadata

		hit := SearchHit{
			Business:   &b,
			Similarity: similarity,
			Snippet:    snippet,
		}
		if priority != nil {
			hit.Score = &domain.LeadScore{
				BusinessID:      b.ID,
				TotalScore:      i32(totalScore),
				Priority:        *priority,
				SalesSuggestion: str(suggestion),
			}
		}
		hits = append(hits, hit)
	}
	return hits, rows.Err()
}

// KeywordSearch is the fallback when no embedding provider is configured, so the
// search box is never simply broken. It is a plain ILIKE, not a pretend vector
// search: the API says which one ran, because the results are not comparable.
func (r *EmbeddingRepo) KeywordSearch(ctx context.Context, q string, f SearchFilter) ([]SearchHit, error) {
	limit := f.Limit
	if limit <= 0 || limit > 100 {
		limit = 20
	}

	args := []any{"%" + q + "%"}
	var where []string

	add := func(clause string, val any) {
		args = append(args, val)
		where = append(where, fmt.Sprintf(clause, len(args)))
	}
	if f.Priority != "" {
		add("s.priority = $%d", f.Priority)
	}
	if f.Category != "" {
		add("b.category ILIKE $%d", "%"+f.Category+"%")
	}

	clause := ""
	if len(where) > 0 {
		clause = " AND " + strings.Join(where, " AND ")
	}

	args = append(args, limit)

	rows, err := r.pool.Query(ctx, fmt.Sprintf(`
		SELECT b.id, b.name, b.address, b.phone, b.rating, b.website, b.category,
		       b.source, b.metadata, b.created_at, b.updated_at,
		       s.total_score, s.priority, s.sales_suggestion
		FROM businesses b
		LEFT JOIN lead_scores s ON s.business_id = b.id
		WHERE (b.name ILIKE $1 OR b.category ILIKE $1 OR b.address ILIKE $1)%s
		ORDER BY s.total_score DESC NULLS LAST
		LIMIT $%d`, clause, len(args)), args...)
	if err != nil {
		return nil, fmt.Errorf("keyword search: %w", err)
	}
	defer rows.Close()

	var hits []SearchHit
	for rows.Next() {
		var (
			b                 domain.Business
			addr, phone       *string
			website, category *string
			rating            *float64
			metadata          []byte
			totalScore        *int32
			priority          *string
			suggestion        *string
		)

		err := rows.Scan(&b.ID, &b.Name, &addr, &phone, &rating, &website, &category,
			&b.Source, &metadata, &b.CreatedAt, &b.UpdatedAt,
			&totalScore, &priority, &suggestion)
		if err != nil {
			return nil, fmt.Errorf("scan keyword hit: %w", err)
		}

		b.Address, b.Phone = str(addr), str(phone)
		b.Website, b.Category = str(website), str(category)
		b.Rating = f64(rating)
		b.Metadata = metadata

		hit := SearchHit{Business: &b}
		if priority != nil {
			hit.Score = &domain.LeadScore{
				BusinessID:      b.ID,
				TotalScore:      i32(totalScore),
				Priority:        *priority,
				SalesSuggestion: str(suggestion),
			}
		}
		hits = append(hits, hit)
	}
	return hits, rows.Err()
}

// Count reports how many leads are embedded, so the UI can say whether semantic
// search is actually ready.
func (r *EmbeddingRepo) Count(ctx context.Context) (int64, error) {
	var n int64
	err := r.pool.QueryRow(ctx, `SELECT count(*) FROM lead_embeddings`).Scan(&n)
	return n, err
}

// vector renders a []float32 in pgvector's literal syntax, '[1,2,3]'.
//
// pgx has no native codec for the vector type, so the value has to arrive as
// text. strconv with 'f' and -1 precision gives the shortest representation that
// round-trips exactly, which matters: a lossy float here silently shifts every
// similarity score.
func vector(v []float32) string {
	if len(v) == 0 {
		return "[]"
	}

	var b strings.Builder
	b.Grow(len(v) * 12)
	b.WriteByte('[')

	for i, f := range v {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatFloat(float64(f), 'f', -1, 32))
	}
	b.WriteByte(']')

	return b.String()
}
