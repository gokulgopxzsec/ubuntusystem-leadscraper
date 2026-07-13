package postgres

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/makeforme/leadscraper/internal/domain"
)

type BusinessRepo struct {
	pool *pgxpool.Pool
}

func NewBusinessRepo(pool *pgxpool.Pool) *BusinessRepo {
	return &BusinessRepo{pool: pool}
}

const businessCols = `id, name, address, phone, rating, website, latitude, longitude,
	category, source_id, source, source_key, metadata, created_at, updated_at`

func (r *BusinessRepo) Create(ctx context.Context, b *domain.Business) error {
	var lat, lng *float64
	if b.Coordinates != nil {
		lat, lng = &b.Coordinates.Lat, &b.Coordinates.Lng
	}

	err := r.pool.QueryRow(ctx, `
		INSERT INTO businesses
			(name, address, phone, rating, website, latitude, longitude,
			 category, source_id, source, source_key, metadata)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
		RETURNING id, created_at, updated_at`,
		b.Name, nullStr(b.Address), nullStr(b.Phone), b.Rating, nullStr(b.Website),
		lat, lng, nullStr(b.Category), nullStr(b.SourceID), b.Source,
		nullStr(b.SourceKey), jsonOrEmpty(b.Metadata),
	).Scan(&b.ID, &b.CreatedAt, &b.UpdatedAt)
	if err != nil {
		return fmt.Errorf("insert business: %w", err)
	}
	return nil
}

func (r *BusinessRepo) GetByID(ctx context.Context, id string) (*domain.Business, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+businessCols+` FROM businesses WHERE id = $1`, id)
	if err != nil {
		return nil, fmt.Errorf("query business: %w", err)
	}
	defer rows.Close()

	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("query business: %w", err)
		}
		return nil, ErrNotFound
	}
	return scanBusiness(rows)
}

func (r *BusinessRepo) List(ctx context.Context, f domain.BusinessFilter) ([]*domain.Business, int64, error) {
	var (
		where []string
		args  []any
	)
	add := func(clause string, val any) {
		args = append(args, val)
		where = append(where, fmt.Sprintf(clause, len(args)))
	}

	if f.Category != "" {
		add("category = $%d", f.Category)
	}
	if f.Source != "" {
		add("source = $%d", f.Source)
	}
	if f.Location != "" {
		add("address ILIKE $%d", "%"+f.Location+"%")
	}
	if f.Search != "" {
		add("name ILIKE $%d", "%"+f.Search+"%")
	}
	if f.HasWebsite != nil {
		if *f.HasWebsite {
			where = append(where, "website IS NOT NULL AND website <> ''")
		} else {
			where = append(where, "(website IS NULL OR website = '')")
		}
	}

	clause := ""
	if len(where) > 0 {
		clause = " WHERE " + strings.Join(where, " AND ")
	}

	var total int64
	if err := r.pool.QueryRow(ctx, `SELECT count(*) FROM businesses`+clause, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count businesses: %w", err)
	}

	limit, offset := paginate(f.Page, f.Limit)
	args = append(args, limit, offset)

	query := fmt.Sprintf(`SELECT %s FROM businesses%s ORDER BY %s LIMIT $%d OFFSET $%d`,
		businessCols, clause, businessSort(f.SortBy, f.SortOrder), len(args)-1, len(args))

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("list businesses: %w", err)
	}
	defer rows.Close()

	var out []*domain.Business
	for rows.Next() {
		b, err := scanBusiness(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, b)
	}
	return out, total, rows.Err()
}

func (r *BusinessRepo) Update(ctx context.Context, b *domain.Business) error {
	var lat, lng *float64
	if b.Coordinates != nil {
		lat, lng = &b.Coordinates.Lat, &b.Coordinates.Lng
	}

	tag, err := r.pool.Exec(ctx, `
		UPDATE businesses SET
			name = $2, address = $3, phone = $4, rating = $5, website = $6,
			latitude = $7, longitude = $8, category = $9, metadata = $10,
			updated_at = now()
		WHERE id = $1`,
		b.ID, b.Name, nullStr(b.Address), nullStr(b.Phone), b.Rating,
		nullStr(b.Website), lat, lng, nullStr(b.Category), jsonOrEmpty(b.Metadata),
	)
	if err != nil {
		return fmt.Errorf("update business: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *BusinessRepo) Delete(ctx context.Context, id string) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM businesses WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete business: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// FindDuplicates matches on any of the three identifying fields. Phone is
// compared on digits only, so "+91 98765 43210" and "09876543210" collide the
// way a human would expect.
func (r *BusinessRepo) FindDuplicates(ctx context.Context, name, phone, website string) ([]*domain.Business, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+businessCols+` FROM businesses
		WHERE ($1 <> '' AND lower(name) = lower($1))
		   OR ($2 <> '' AND right(regexp_replace(coalesce(phone,''), '\D', '', 'g'), 10)
		                  = right(regexp_replace($2, '\D', '', 'g'), 10)
		        AND length(regexp_replace(coalesce(phone,''), '\D', '', 'g')) >= 10)
		   OR ($3 <> '' AND lower(coalesce(website,'')) = lower($3))
		LIMIT 50`,
		name, phone, website)
	if err != nil {
		return nil, fmt.Errorf("find duplicates: %w", err)
	}
	defer rows.Close()

	var out []*domain.Business
	for rows.Next() {
		b, err := scanBusiness(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// BulkInsert upserts on the (source, source_key) dedup index and reports how
// many rows were new. Records without a source_key cannot be deduped, so they
// are inserted plainly.
func (r *BusinessRepo) BulkInsert(ctx context.Context, businesses []*domain.Business) (int, error) {
	if len(businesses) == 0 {
		return 0, nil
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin bulk insert: %w", err)
	}
	defer tx.Rollback(ctx)

	inserted := 0
	for _, b := range businesses {
		var lat, lng *float64
		if b.Coordinates != nil {
			lat, lng = &b.Coordinates.Lat, &b.Coordinates.Lng
		}

		var isNew bool
		err := tx.QueryRow(ctx, `
			INSERT INTO businesses
				(name, address, phone, rating, website, latitude, longitude,
				 category, source_id, source, source_key, metadata)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
			ON CONFLICT (source, source_key) WHERE source_key IS NOT NULL
			DO UPDATE SET
				name = EXCLUDED.name,
				address = COALESCE(EXCLUDED.address, businesses.address),
				phone = COALESCE(EXCLUDED.phone, businesses.phone),
				rating = EXCLUDED.rating,
				website = COALESCE(EXCLUDED.website, businesses.website),
				latitude = COALESCE(EXCLUDED.latitude, businesses.latitude),
				longitude = COALESCE(EXCLUDED.longitude, businesses.longitude),
				category = COALESCE(EXCLUDED.category, businesses.category),
				metadata = EXCLUDED.metadata,
				updated_at = now()
			RETURNING id, (xmax = 0) AS is_new`,
			b.Name, nullStr(b.Address), nullStr(b.Phone), b.Rating, nullStr(b.Website),
			lat, lng, nullStr(b.Category), nullStr(b.SourceID), b.Source,
			nullStr(b.SourceKey), jsonOrEmpty(b.Metadata),
		).Scan(&b.ID, &isNew)
		if err != nil {
			return 0, fmt.Errorf("upsert business %q: %w", b.Name, err)
		}
		if isNew {
			inserted++
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit bulk insert: %w", err)
	}
	return inserted, nil
}

func scanBusiness(rows pgx.Rows) (*domain.Business, error) {
	var (
		b                 domain.Business
		address, phone    *string
		website, category *string
		sourceID, srcKey  *string
		rating            *float64
		lat, lng          *float64
		metadata          []byte
	)

	err := rows.Scan(&b.ID, &b.Name, &address, &phone, &rating, &website,
		&lat, &lng, &category, &sourceID, &b.Source, &srcKey, &metadata,
		&b.CreatedAt, &b.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("scan business: %w", err)
	}

	b.Address = str(address)
	b.Phone = str(phone)
	b.Rating = f64(rating)
	b.Website = str(website)
	b.Category = str(category)
	b.SourceID = str(sourceID)
	b.SourceKey = str(srcKey)
	b.Metadata = metadata

	if lat != nil && lng != nil {
		b.Coordinates = &domain.Coordinates{Lat: *lat, Lng: *lng}
	}
	return &b, nil
}

func paginate(page, limit int) (int, int) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if page < 1 {
		page = 1
	}
	return limit, (page - 1) * limit
}

// businessSort maps user input to a fixed set of columns. The sort column
// cannot be parameterised, so it must never be interpolated from raw input.
func businessSort(sortBy, order string) string {
	col, ok := map[string]string{
		"name":       "name",
		"rating":     "rating",
		"created_at": "created_at",
		"updated_at": "updated_at",
	}[sortBy]
	if !ok {
		col = "created_at"
	}

	dir := "DESC"
	if strings.EqualFold(order, "asc") {
		dir = "ASC"
	}
	return col + " " + dir
}
