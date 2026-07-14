-- A boolean "reachable" was not enough to describe what a crawl found.
--
-- Any status >= 400 was treated as unreachable, and a robots.txt disallow was
-- treated the same way. Both then fired the broken_website rule, worth 25 points
-- and the line "their website does not load". So a site behind Cloudflare that
-- answered our bot with 403, and a site whose robots.txt politely asked us not to
-- look, were both reported to the sales team as broken.
--
-- They are not broken. They are up. Telling a business their website is down when
-- it plainly is not is the kind of mistake that ends a call in the first ten
-- seconds, so the crawl now records what it actually learned.
ALTER TABLE websites
    ADD COLUMN crawl_status TEXT NOT NULL DEFAULT 'unknown';

-- Backfill from the status codes we already stored. Rows with no code were never
-- fetched (a DNS failure, or robots), and those two are indistinguishable after
-- the fact -- so they stay 'unknown' rather than being guessed at, and will
-- resolve on the next crawl.
UPDATE websites SET crawl_status = CASE
    WHEN status_code BETWEEN 200 AND 399              THEN 'live'
    WHEN status_code IN (401, 403, 405, 406, 429, 451) THEN 'blocked'
    WHEN status_code >= 500                            THEN 'down'
    WHEN status_code IN (404, 410)                     THEN 'down'
    ELSE 'unknown'
END;

CREATE INDEX idx_websites_crawl_status ON websites(crawl_status);
