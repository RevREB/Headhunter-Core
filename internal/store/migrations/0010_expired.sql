-- Days-on-board / expired. A gated background loop re-probes tracked posting
-- URLs; after K consecutive definitive 404/410 probes a listing is marked gone
-- (disappeared_at). Passive roles (never engaged / passed on) then move to the
-- terminal 'expired' status; active-pipeline roles keep their status but still
-- get disappeared_at stamped, so the per-company "days listed" stat is complete
-- regardless of the user's funnel. ADD VALUE runs fine inside this migration's
-- txn on PG12+ (the value isn't used until a later, separate transaction).
ALTER TYPE application_status ADD VALUE IF NOT EXISTS 'expired';

ALTER TABLE applications ADD COLUMN IF NOT EXISTS disappeared_at timestamptz;
ALTER TABLE applications ADD COLUMN IF NOT EXISTS gone_probes    int NOT NULL DEFAULT 0;
ALTER TABLE applications ADD COLUMN IF NOT EXISTS last_probed_at timestamptz;

-- The re-probe worklist: canonical roles with a live URL not yet marked gone,
-- least-recently-probed first.
CREATE INDEX IF NOT EXISTS applications_reprobe_idx
    ON applications (last_probed_at NULLS FIRST)
    WHERE url IS NOT NULL AND disappeared_at IS NULL AND canonical_id IS NULL;
