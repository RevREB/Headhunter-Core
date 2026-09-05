-- Inbox as a work queue: a persistent evaluator pool claims postings atomically.
-- eval_claimed_at marks a posting a worker has taken (so others skip it); it is
-- cleared/irrelevant once the posting leaves 'inbox'. Stale claims (crashed
-- worker) are reclaimed by age.
ALTER TABLE applications ADD COLUMN IF NOT EXISTS eval_claimed_at timestamptz;
CREATE INDEX IF NOT EXISTS applications_eval_queue_idx
    ON applications (status, eval_claimed_at) WHERE canonical_id IS NULL;
