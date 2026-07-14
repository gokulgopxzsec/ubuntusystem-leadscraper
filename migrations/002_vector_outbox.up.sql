-- Semantic search, a transactional outbox, and the constraint that should have
-- been on websites from the start.

-- ---------------------------------------------------------------- pgvector

CREATE EXTENSION IF NOT EXISTS vector;

-- ---------------------------------------------------------------- websites

-- websites had no unique constraint on business_id, and the crawl handler did a
-- plain INSERT. Every retry or re-scrape appended another row: GetByBusinessID
-- hid it behind ORDER BY created_at DESC LIMIT 1, so the table grew silently and
-- old crawl_results pointed at website rows nobody would ever read again.
--
-- Collapse the duplicates onto the newest row, moving the children with them,
-- then add the constraint so it cannot happen again.
WITH ranked AS (
    SELECT id,
           business_id,
           first_value(id) OVER (
               PARTITION BY business_id ORDER BY created_at DESC, id DESC
           ) AS keep_id
    FROM websites
)
UPDATE crawl_results c
SET website_id = r.keep_id
FROM ranked r
WHERE c.website_id = r.id AND r.id <> r.keep_id;

WITH ranked AS (
    SELECT id,
           business_id,
           first_value(id) OVER (
               PARTITION BY business_id ORDER BY created_at DESC, id DESC
           ) AS keep_id
    FROM websites
)
UPDATE technologies t
SET website_id = r.keep_id
FROM ranked r
WHERE t.website_id = r.id AND r.id <> r.keep_id;

WITH ranked AS (
    SELECT id,
           business_id,
           first_value(id) OVER (
               PARTITION BY business_id ORDER BY created_at DESC, id DESC
           ) AS keep_id
    FROM websites
)
UPDATE audit_reports a
SET website_id = r.keep_id
FROM ranked r
WHERE a.website_id = r.id AND r.id <> r.keep_id;

DELETE FROM websites w
WHERE EXISTS (
    SELECT 1 FROM websites newer
    WHERE newer.business_id = w.business_id
      AND (newer.created_at, newer.id) > (w.created_at, w.id)
);

ALTER TABLE websites ADD CONSTRAINT websites_business_id_key UNIQUE (business_id);

-- ---------------------------------------------------------------- outbox

-- Redis cannot join a Postgres transaction, so "insert the businesses, then
-- enqueue their jobs" has a window: if the worker dies between the two, the
-- businesses exist and no job will ever crawl or score them. They are not
-- failed, they are invisible.
--
-- Jobs are now written into this table inside the same transaction as the data
-- they refer to, and a relay moves them to Redis afterwards. A crash can only
-- ever cause a job to be delivered twice, never zero times, which is why the
-- handlers have to be idempotent.
CREATE TABLE job_outbox (
    id           BIGSERIAL PRIMARY KEY,
    payload      JSONB       NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    published_at TIMESTAMPTZ,
    attempts     INTEGER     NOT NULL DEFAULT 0,
    last_error   TEXT
);

-- The relay only ever reads the unpublished tail, so the index covers just that.
CREATE INDEX idx_outbox_unpublished ON job_outbox (id)
    WHERE published_at IS NULL;

-- ---------------------------------------------------------------- embeddings

-- One row per business: the text we embedded, and its vector.
--
-- 768 dimensions is not arbitrary. Gemini's text-embedding-004 is natively 768,
-- and OpenAI's text-embedding-3-small can be asked for 768 via its dimensions
-- parameter. Fixing the column at one size means either provider can fill it and
-- the index stays valid if you switch.
CREATE TABLE lead_embeddings (
    business_id UUID PRIMARY KEY REFERENCES businesses(id) ON DELETE CASCADE,
    content     TEXT         NOT NULL,
    embedding   vector(768)  NOT NULL,
    model       TEXT         NOT NULL,
    -- The hash of the source text. Re-embedding an unchanged lead is a wasted
    -- API call, and at a few thousand leads that adds up.
    content_hash TEXT        NOT NULL,
    updated_at  TIMESTAMPTZ  NOT NULL DEFAULT now()
);

-- IVFFlat needs training data to build a useful index and is counterproductive
-- on a small table, so this starts unindexed: with a few thousand leads a exact
-- scan is fast, and correct. Add the index when the corpus justifies it:
--
--   CREATE INDEX ON lead_embeddings
--     USING ivfflat (embedding vector_cosine_ops) WITH (lists = 100);
--
-- cosine, not L2: the providers return normalised vectors and cosine is what
-- their similarity is defined against.
