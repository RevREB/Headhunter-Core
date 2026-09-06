-- Per-source scrape ledger (scraper contract v1.1). One row per ATS per ingest
-- batch: "source X was scraped at T with N postings, ok=true". This is the
-- ground truth a future source-scoped set-diff needs — a posting last seen on
-- source X but absent from a later SUCCESSFUL scrape of X is a gone candidate
-- (reliable for full-board ATS, unlike the URL re-probe). Also powers per-ATS
-- scan analytics.
CREATE TABLE IF NOT EXISTS scan_runs (
    id       bigserial PRIMARY KEY,
    ats      text NOT NULL,
    postings int NOT NULL DEFAULT 0,
    ok       boolean NOT NULL DEFAULT true,
    at       timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS scan_runs_ats_at_idx ON scan_runs (ats, at DESC);
