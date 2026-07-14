DROP INDEX IF EXISTS idx_websites_crawl_status;
ALTER TABLE websites DROP COLUMN IF EXISTS crawl_status;
