-- Headhunter Core — initial schema (Postgres 16+, CNPG).
-- Relational spine where we query/aggregate; JSONB where the shape is variable.

CREATE TYPE application_status AS ENUM (
    'evaluated','applied','responded','interview','offer','hired','rejected','discarded','skip'
);

CREATE TABLE applications (
    id         bigserial PRIMARY KEY,
    url        text UNIQUE,
    company    text NOT NULL,
    role       text NOT NULL,
    score      numeric(3,1),
    status     application_status NOT NULL DEFAULT 'evaluated',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- the transition ledger (append-only): the analytics spine for funnel/velocity
CREATE TABLE status_events (
    id             bigserial PRIMARY KEY,
    application_id bigint NOT NULL REFERENCES applications(id) ON DELETE CASCADE,
    from_status    application_status,
    to_status      application_status NOT NULL,
    source         text,
    note           text,
    at             timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX ON status_events (application_id, at);

-- append-only event ledgers (each FK -> application)
CREATE TABLE scan_sightings (
    id             bigserial PRIMARY KEY,
    application_id bigint REFERENCES applications(id) ON DELETE SET NULL,
    url            text NOT NULL,
    ats            text,
    fingerprint    bytea,        -- SimHash of the JD, for repost detection
    trust_score    int,
    first_seen     timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX ON scan_sightings (url);

CREATE TABLE salary_observations (
    id             bigserial PRIMARY KEY,
    application_id bigint REFERENCES applications(id) ON DELETE CASCADE,
    kind           text,         -- desired | advertised | actual
    amount         numeric,
    currency       text,
    source         text,
    note           text,
    at             timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE assessments (
    id             bigserial PRIMARY KEY,
    application_id bigint REFERENCES applications(id) ON DELETE CASCADE,
    detail         jsonb,
    at             timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE contacts (
    id             bigserial PRIMARY KEY,
    application_id bigint REFERENCES applications(id) ON DELETE SET NULL,
    detail         jsonb,
    updated_at     timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE follow_ups (
    id             bigserial PRIMARY KEY,
    application_id bigint REFERENCES applications(id) ON DELETE CASCADE,
    due            timestamptz,
    pinned         boolean NOT NULL DEFAULT false,
    note           text
);

-- variable-shape documents -> JSONB (covers Mongo's only advantage in one engine)
CREATE TABLE postings (
    id             bigserial PRIMARY KEY,
    application_id bigint REFERENCES applications(id) ON DELETE CASCADE,
    doc            jsonb NOT NULL         -- raw scraped posting
);
CREATE TABLE reports (
    id             bigserial PRIMARY KEY,
    application_id bigint REFERENCES applications(id) ON DELETE CASCADE,
    doc            jsonb NOT NULL         -- eval: score/fit/legitimacy + machine-summary arrays
);
CREATE TABLE config (
    key            text PRIMARY KEY,      -- 'profile', 'portals', ...
    doc            jsonb NOT NULL,
    updated_at     timestamptz NOT NULL DEFAULT now()
);
