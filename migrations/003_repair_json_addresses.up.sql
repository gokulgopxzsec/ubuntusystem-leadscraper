-- gosom's `complete_address` column is a JSON object, not a string — the one
-- column in its output that is not plain text. The Maps adapter preferred it
-- over the plain `address` column on the assumption it was richer, so every
-- scraped address was stored as a raw {"borough":...,"street":...} blob:
-- unreadable in the dashboard, useless in a CSV export.
--
-- The adapter now prefers the plain column and flattens the JSON only as a
-- fallback. This repairs the rows already written.

-- A plain `address::jsonb` in an UPDATE would abort the whole migration the
-- first time it met a row that only looked like JSON. Casting inside a function
-- that traps the failure keeps one bad row from taking the migration with it.
CREATE OR REPLACE FUNCTION pg_temp.flatten_address(raw text)
RETURNS text AS $$
DECLARE
    doc jsonb;
BEGIN
    BEGIN
        doc := raw::jsonb;
    EXCEPTION WHEN others THEN
        RETURN raw;      -- not JSON after all; leave it exactly as it was
    END;

    IF jsonb_typeof(doc) <> 'object' THEN
        RETURN raw;
    END IF;

    RETURN nullif(
        concat_ws(', ',
            nullif(trim(doc ->> 'street'), ''),
            nullif(trim(doc ->> 'borough'), ''),
            nullif(trim(doc ->> 'city'), ''),
            nullif(trim(doc ->> 'state'), ''),
            nullif(trim(doc ->> 'postal_code'), '')
        ), '');
END;
$$ LANGUAGE plpgsql IMMUTABLE;

UPDATE businesses
SET address = pg_temp.flatten_address(address),
    updated_at = now()
WHERE address LIKE '{%';
