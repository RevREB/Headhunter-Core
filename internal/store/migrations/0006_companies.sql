-- Companies as a first-class entity, so a company's OSINT profile is computed
-- once (by the profiler consumer) and reused across all its postings. A new row
-- (profiled_at IS NULL, profile_claimed_at IS NULL) is what the consumer drains.
-- The profile is assembled from free structured sources and is provenance-tagged
-- per field; flags carry deterministic exclusions (e.g. casino).
CREATE TABLE IF NOT EXISTS companies (
    id                 bigserial PRIMARY KEY,
    norm               text NOT NULL UNIQUE,          -- engine.NormalizeCompany(name); the join key
    name               text NOT NULL DEFAULT '',
    domain             text,                           -- company website host, once discovered
    profile            jsonb NOT NULL DEFAULT '{}'::jsonb,
    flags              text[] NOT NULL DEFAULT '{}',
    profiled_at        timestamptz,                    -- last successful profile (TTL/freshness)
    profile_claimed_at timestamptz,                    -- work-queue claim (FOR UPDATE SKIP LOCKED)
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now()
);

-- Link postings to their company. Populated transactionally at ingest and by a
-- one-time Go backfill (which uses NormalizeCompany so norms stay consistent).
ALTER TABLE applications ADD COLUMN IF NOT EXISTS company_id bigint REFERENCES companies(id);
CREATE INDEX IF NOT EXISTS applications_company_id_idx ON applications (company_id);

-- Profiler work-queue: unprofiled, unclaimed companies first.
CREATE INDEX IF NOT EXISTS companies_profile_queue_idx ON companies (profiled_at, profile_claimed_at);
