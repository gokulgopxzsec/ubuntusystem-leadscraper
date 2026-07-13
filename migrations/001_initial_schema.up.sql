CREATE TABLE sources (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name        TEXT NOT NULL UNIQUE,
    type        TEXT NOT NULL DEFAULT 'scraper',
    enabled     BOOLEAN DEFAULT true,
    config      JSONB DEFAULT '{}',
    last_run_at TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE businesses (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name        TEXT NOT NULL,
    address     TEXT,
    phone       TEXT,
    rating      REAL,
    website     TEXT,
    latitude    DOUBLE PRECISION,
    longitude   DOUBLE PRECISION,
    category    TEXT,
    source_id   UUID REFERENCES sources(id) ON DELETE SET NULL,
    source      TEXT NOT NULL,
    source_key  TEXT,
    metadata    JSONB DEFAULT '{}',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE websites (
    id                 UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    business_id        UUID NOT NULL REFERENCES businesses(id) ON DELETE CASCADE,
    url                TEXT NOT NULL,
    status_code        INTEGER,
    load_time_ms       INTEGER,
    has_ssl            BOOLEAN DEFAULT FALSE,
    has_booking        BOOLEAN DEFAULT FALSE,
    is_mobile_friendly BOOLEAN DEFAULT FALSE,
    pages_crawled      INTEGER DEFAULT 0,
    title              TEXT,
    meta_description   TEXT,
    crawled_at         TIMESTAMPTZ,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE crawl_results (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    website_id  UUID NOT NULL REFERENCES websites(id) ON DELETE CASCADE,
    url         TEXT NOT NULL,
    status_code INTEGER,
    html        TEXT,
    title       TEXT,
    meta_tags   JSONB DEFAULT '{}',
    links       TEXT[] DEFAULT '{}',
    error       TEXT,
    crawled_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE technologies (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    website_id  UUID NOT NULL REFERENCES websites(id) ON DELETE CASCADE,
    name        TEXT NOT NULL,
    version     TEXT,
    category    TEXT NOT NULL DEFAULT 'unknown',
    detected_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE contacts (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    business_id  UUID NOT NULL REFERENCES businesses(id) ON DELETE CASCADE,
    email        TEXT,
    phone        TEXT,
    whatsapp     TEXT,
    contact_type TEXT NOT NULL DEFAULT 'general',
    source       TEXT,
    confidence   REAL DEFAULT 0,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE social_profiles (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    business_id UUID NOT NULL REFERENCES businesses(id) ON DELETE CASCADE,
    platform    TEXT NOT NULL,
    url         TEXT NOT NULL,
    followers   INTEGER DEFAULT 0,
    verified    BOOLEAN DEFAULT FALSE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(business_id, platform)
);

CREATE TABLE audit_reports (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    website_id       UUID REFERENCES websites(id) ON DELETE SET NULL,
    business_id      UUID NOT NULL REFERENCES businesses(id) ON DELETE CASCADE,
    quality_score    INTEGER DEFAULT 0,
    seo_score        INTEGER DEFAULT 0,
    mobile_score     INTEGER DEFAULT 0,
    issues           TEXT[] DEFAULT '{}',
    recommendations  TEXT[] DEFAULT '{}',
    summary          TEXT,
    services_to_offer TEXT[] DEFAULT '{}',
    audited_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE lead_scores (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    business_id      UUID NOT NULL REFERENCES businesses(id) ON DELETE CASCADE,
    total_score      INTEGER NOT NULL DEFAULT 0,
    rule_score       INTEGER DEFAULT 0,
    ai_score         INTEGER DEFAULT 0,
    priority         TEXT NOT NULL DEFAULT 'low',
    breakdown        JSONB DEFAULT '{}',
    sales_suggestion TEXT,
    scored_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(business_id)
);

CREATE TABLE scrape_jobs (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    source        TEXT NOT NULL,
    category      TEXT,
    location      TEXT,
    status        TEXT NOT NULL DEFAULT 'pending',
    total_found   INTEGER DEFAULT 0,
    success_count INTEGER DEFAULT 0,
    fail_count    INTEGER DEFAULT 0,
    params        JSONB DEFAULT '{}',
    started_at    TIMESTAMPTZ,
    completed_at  TIMESTAMPTZ,
    error         TEXT,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_businesses_category ON businesses(category);
CREATE INDEX idx_businesses_source ON businesses(source);
CREATE INDEX idx_businesses_website ON businesses(website) WHERE website IS NOT NULL;
CREATE INDEX idx_businesses_source_key ON businesses(source_key);
CREATE UNIQUE INDEX idx_businesses_dedup ON businesses(source, source_key) WHERE source_key IS NOT NULL;

CREATE INDEX idx_websites_business ON websites(business_id);
CREATE INDEX idx_crawl_results_website ON crawl_results(website_id);
CREATE INDEX idx_technologies_website ON technologies(website_id);
CREATE INDEX idx_technologies_name ON technologies(name);
CREATE INDEX idx_contacts_business ON contacts(business_id);
CREATE INDEX idx_social_business ON social_profiles(business_id);
CREATE INDEX idx_audit_business ON audit_reports(business_id);
CREATE INDEX idx_scores_business ON lead_scores(business_id);
CREATE INDEX idx_scores_priority ON lead_scores(priority) WHERE priority IN ('high', 'medium');
CREATE INDEX idx_jobs_status ON scrape_jobs(status);
CREATE INDEX idx_jobs_source ON scrape_jobs(source);
