-- Fresh scraped postings land in 'inbox' (unscored) until evaluated.
ALTER TYPE application_status ADD VALUE IF NOT EXISTS 'inbox' BEFORE 'evaluated';
