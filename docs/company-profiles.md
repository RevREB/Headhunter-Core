# Company profiles (the company graph)

Companies are a first-class entity in Headhunter. A company's OSINT profile is
assembled **once** from free, structured sources and reused across every posting
from that company — so scoring gets consistent background facts the JD can't
provide, at near-zero marginal cost.

## Why

- **Token amortization.** A company appears in many postings. Sourcing its
  profile once (deterministically, no LLM) and reusing it beats re-deriving
  company context per job.
- **Calibration.** The JD says what the role is; the profile says what the JD
  won't — stage/stability, size, public/private, tech/ATS, reputation.
- **Rule enforcement.** Deterministic `flags` (e.g. `casino`) let the evaluator
  hard-stop excluded companies without spending an LLM call.
- **Library expansion.** Each profile records the company's ATS (inferred from
  its postings), turning "new company seen" into "add it to the per-ATS scraper."

## Cost model

Sourcing is **100% deterministic** (HTTP + parse) — zero tokens. The only token
cost is an optional **synthesis pass** (a narrative summary + soft-signal
inference), which is cached per company, runs on a cheap model, and is off the
critical path. The structured, provenance-tagged fields are usable the instant
sourcing completes, tokens or not. (Synthesis is Phase 2.)

## Data model

- `companies` — `id`, `norm` (unique; `engine.NormalizeCompany`, the join key),
  `name`, `domain`, `profile jsonb`, `flags text[]`, `profiled_at`,
  `profile_claimed_at`, timestamps. (migration `0006_companies.sql`)
- `applications.company_id` — set transactionally at ingest and by a one-time Go
  backfill (`BackfillCompanies`, which uses `NormalizeCompany` so norms match).

Profile shape (provenance-tagged per field; `source:"inferred"` = LLM guess,
never gate-eligible):

```json
{
  "fields": {
    "founded":   {"value": 1925,   "source": "wikidata"},
    "employees": {"value": 85116,  "source": "wikidata"},
    "hq":        {"value": "Irving, TX, US", "source": "builtin"},
    "website":   {"value": "https://www.caterpillar.com/", "source": "wikidata"},
    "industry":  {"value": ["Cloud","Industrial"], "source": "builtin"},
    "stage":     {"value": "public", "source": "sec_edgar", "detail": "CIK 0000018230"},
    "ticker":    {"value": "CAT", "source": "sec_edgar"},
    "ats":       {"value": "greenhouse", "source": "hh_derived", "detail": "boards.greenhouse.io/caterpillar"}
  },
  "summary": "",
  "sourcesTried": ["builtin","wikidata","sec_edgar","ats"]
}
```

## Enrichers (`internal/enrich`)

Pluggable `Enricher`s; a source (incl. a future licensed Crunchbase API) is one
file. Fields merge by authority: `sec_edgar > wikidata > builtin/hh_derived >
inferred`.

| Enricher | Access | Fields |
|---|---|---|
| `builtin` | free; URL from a posting's `hiringOrganization.sameAs` | founded, employees, HQ, industry, **website**, description |
| `wikidata` | free REST; matched on **domain** to avoid mis-hits | founded, employees, website |
| `sec_edgar` | free `company_tickers.json`; needs a contact UA (see below) | stage=public, ticker, CIK |
| `ats` | derived from our own stored postings | ATS + board host (library-expansion signal) |

**SEC EDGAR requires a `User-Agent` carrying a contact email** (URL-only/browser
UAs are 403'd). Set `ENRICH_UA="<name> <email>"` on headhunter-core to activate
it; without it, EDGAR no-ops and the other three sources still produce a rich
profile.

## Consumer

`RunProfiler` is a persistent, DB-claimed pub/sub consumer (same pattern as the
evaluator): it drains never-profiled companies, N workers from Config
`company_profile_concurrency` (default 2), pausable via `profile_enabled`,
resumes after restart. Companies only exist because a keyword-filtered posting
created them, so the queue is naturally bounded to relevant firms.

API: `GET /api/companies`, `GET /api/company/{id}`,
`GET /api/companies/profile/status`, `POST /api/companies/profile/pause`,
`POST /api/companies/reprofile`. UI: **Companies** list + detail (profile fields
with source badges, the company's postings).

## Roadmap

1. **Done** — entity + link + backfill; deterministic sourcing consumer; the
   four free enrichers; Companies list/detail UI.
2. Evaluator integration — inject the provenance-marked profile into eval
   context; deterministic exclusion gate (flags → hard-stop, no LLM); cheap lazy
   synthesis pass for the narrative.
3. Graph **edges** (`company_edges`: same_ats, same_industry, shared_investor,
   competitor) → "companies similar to ones I scored ≥4" sourcing.
4. Crunchbase enricher (licensed) — drops in as one more provenance source.
