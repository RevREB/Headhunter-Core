-- reports/postings are looked up by application_id on every job view, the audit,
-- and the remediation, but were unindexed — forcing sequential scans (the
-- remediation dry-run timed out). Index them.
CREATE INDEX IF NOT EXISTS reports_application_id_idx ON reports (application_id);
CREATE INDEX IF NOT EXISTS postings_application_id_idx ON postings (application_id);
