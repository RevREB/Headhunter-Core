-- Capture WHY a role was passed on. Below-the-line (score < apply_min_score) is
-- derived from the score; a pass-over (above the line, declined by a human) needs
-- the reason recorded so it can tune the evaluator. Nullable: only set when the
-- human gives one.
ALTER TABLE applications ADD COLUMN IF NOT EXISTS disposition_reason text;
