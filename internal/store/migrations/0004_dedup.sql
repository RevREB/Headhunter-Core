-- Near-duplicate merge: the same role posted under many URLs (multi-location
-- listings, reposts) should collapse to one canonical application. We key on a
-- normalized company+title (dedup_key) narrowed by SimHash of the JD content
-- (fingerprint). A near-dup carries canonical_id -> its canonical; the worklist
-- (inbox/pipeline/funnel) shows only canonical_id IS NULL, so nothing is deleted.
ALTER TABLE applications ADD COLUMN IF NOT EXISTS dedup_key    text;
ALTER TABLE applications ADD COLUMN IF NOT EXISTS fingerprint  bytea;
ALTER TABLE applications ADD COLUMN IF NOT EXISTS canonical_id bigint REFERENCES applications(id) ON DELETE SET NULL;
CREATE INDEX IF NOT EXISTS applications_dedup_key_idx    ON applications (dedup_key);
CREATE INDEX IF NOT EXISTS applications_canonical_id_idx ON applications (canonical_id);
