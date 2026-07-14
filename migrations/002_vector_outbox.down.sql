DROP TABLE IF EXISTS lead_embeddings;
DROP TABLE IF EXISTS job_outbox;

ALTER TABLE websites DROP CONSTRAINT IF EXISTS websites_business_id_key;

-- The extension is left in place: other schemas may be using it, and dropping it
-- would take their columns with it.
