package ports

import (
	"context"

	"github.com/makeforme/leadscraper/internal/domain"
)

type BusinessRepository interface {
	Create(ctx context.Context, b *domain.Business) error
	GetByID(ctx context.Context, id string) (*domain.Business, error)
	List(ctx context.Context, filter domain.BusinessFilter) ([]*domain.Business, int64, error)
	Update(ctx context.Context, b *domain.Business) error
	Delete(ctx context.Context, id string) error
	FindDuplicates(ctx context.Context, name, phone, website string) ([]*domain.Business, error)
	BulkInsert(ctx context.Context, businesses []*domain.Business) (int, error)
}

type WebsiteRepository interface {
	Create(ctx context.Context, w *domain.Website) error
	GetByBusinessID(ctx context.Context, businessID string) (*domain.Website, error)
	Update(ctx context.Context, w *domain.Website) error
}

type ContactRepository interface {
	Create(ctx context.Context, c *domain.Contact) error
	GetByBusinessID(ctx context.Context, businessID string) ([]*domain.Contact, error)
	BulkUpsert(ctx context.Context, contacts []*domain.Contact) error
}

type SocialProfileRepository interface {
	Create(ctx context.Context, p *domain.SocialProfile) error
	GetByBusinessID(ctx context.Context, businessID string) ([]*domain.SocialProfile, error)
	BulkUpsert(ctx context.Context, profiles []*domain.SocialProfile) error
}

type AuditRepository interface {
	Create(ctx context.Context, a *domain.AuditReport) error
	GetByBusinessID(ctx context.Context, businessID string) (*domain.AuditReport, error)
}

type LeadScoreRepository interface {
	Create(ctx context.Context, s *domain.LeadScore) error
	GetByBusinessID(ctx context.Context, businessID string) (*domain.LeadScore, error)
	ListHighPriority(ctx context.Context, limit int) ([]*domain.LeadScore, error)
	Update(ctx context.Context, s *domain.LeadScore) error
}

type ScrapeJobRepository interface {
	Create(ctx context.Context, j *domain.ScrapeJob) error
	Update(ctx context.Context, j *domain.ScrapeJob) error
	GetByID(ctx context.Context, id string) (*domain.ScrapeJob, error)
	List(ctx context.Context, filter domain.JobFilter) ([]*domain.ScrapeJob, int64, error)
}

type SourceRepository interface {
	Create(ctx context.Context, s *domain.Source) error
	GetByName(ctx context.Context, name string) (*domain.Source, error)
	List(ctx context.Context) ([]*domain.Source, error)
	Update(ctx context.Context, s *domain.Source) error
}

type CrawlResultRepository interface {
	Create(ctx context.Context, r *domain.CrawlResult) error
	GetByWebsiteID(ctx context.Context, websiteID string) (*domain.CrawlResult, error)
}

// TechnologyRepository takes the website id explicitly: domain.Technology is a
// value type embedded in Website and carries no foreign key of its own.
type TechnologyRepository interface {
	CreateForWebsite(ctx context.Context, websiteID string, t *domain.Technology) error
	ReplaceForWebsite(ctx context.Context, websiteID string, techs []domain.Technology) error
	GetByWebsiteID(ctx context.Context, websiteID string) ([]*domain.Technology, error)
	DeleteByWebsiteID(ctx context.Context, websiteID string) error
}
