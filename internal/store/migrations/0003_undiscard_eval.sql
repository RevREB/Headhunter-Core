-- The evaluator must only RATE, never discard — discarding is the user's call.
-- Restore every posting the evaluator auto-discarded (status 'discarded' AND it
-- carries an eval report) back to 'evaluated', preserving its score and report.
-- Imported historical discards have no eval report and are left untouched.
INSERT INTO status_events (application_id, from_status, to_status, source, note)
SELECT a.id, 'discarded'::application_status, 'evaluated'::application_status,
       'migration', 'un-discard: evaluator now rates only'
FROM applications a
WHERE a.status = 'discarded'
  AND EXISTS (SELECT 1 FROM reports r WHERE r.application_id = a.id);

UPDATE applications
SET status = 'evaluated', updated_at = now()
WHERE status = 'discarded'
  AND EXISTS (SELECT 1 FROM reports r WHERE r.application_id = applications.id);
