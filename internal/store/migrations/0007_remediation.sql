-- Data remediation: honest provenance for imported records.
--
-- A scraped record always has a posting row; a career-ops import never does. So
-- "no posting" is the stable marker of an imported record — flag it, so the UI
-- and queries can distinguish a legacy career-ops evaluation from a native A–G
-- one instead of both hiding behind status='evaluated'.
ALTER TABLE applications ADD COLUMN IF NOT EXISTS imported boolean NOT NULL DEFAULT false;

UPDATE applications a
SET imported = true
WHERE NOT EXISTS (SELECT 1 FROM postings p WHERE p.application_id = a.id);
