# Headhunter-Core

The Headhunter engine — a Go + PostgreSQL platform for an automated, self-hosted
job search. Owns everything durable: the data model and status state machine,
merge/dedup, URL normalization, SimHash fingerprinting, trust scoring, repost
detection, SQL analytics, the MCP-shaped API, and the on-demand **scraper operator**.

Part of the **Headhunter** project:
- **Headhunter-Core** (this repo) — engine, Postgres, API, the `Scraper` contract.
- **Headhunter-Scrapers** — the ATS scraper library (versioned one-shot modules).
- **Headhunter-WebMCP** — dashboard + MCP surface over the Core API.

## Architecture

```
client → MCP surface → Headhunter-Core (Go + Postgres, API-first)
                         │
   cycle ────────────────┤ operator-lite: one Kubernetes Job per ATS, from the
                         │   git-declared scraper catalog, scale-to-zero.
                         ▼
           Headhunter-Scrapers/<ats>:vX  (one-shot Job: fetch → RawPostings → exit)
```

The design decision that makes the whole thing maintainable: a scraper's *only*
job is to fetch raw postings from one ATS (`pkg/scraper`). All the durable,
valuable logic stays in Core, so the volatile edge (ATS scraping breaks weekly)
never touches the engine.

**Database:** PostgreSQL with JSONB — a relational spine (`applications` 1→N
`status_events` + event ledgers) for the analytics-heavy workload, and JSONB
columns for the variable-shape documents (postings, evaluation reports, config).

## Layout

| Path | Purpose |
|---|---|
| `pkg/scraper` | the ATS scraper wire contract (v1) — imported by Headhunter-Scrapers |
| `internal/store` | Postgres persistence (pgx) |
| `internal/engine` | state machine, dedup, SimHash, trust; Tier-1 manifest runner |
| `internal/analytics` | SQL funnel/velocity/latency/survival metrics |
| `internal/api` | MCP-shaped HTTP surface |
| `internal/operator` | on-demand scraper Job launcher |
| `migrations` | Postgres schema |

## Status

Early scaffold. See the phased plan: MVP Core (data/state/analytics/API/cycle)
→ scraper library → WebMCP.

## Inspired by

Headhunter is an independent, clean-room implementation inspired by
[career-ops](https://github.com/santifer/career-ops) (MIT © Santiago Fernández
de Valderrama) — an excellent local-first job-search toolkit. Headhunter shares
none of its source; it reimagines the concept as a Go + Postgres platform with a
plugin scraper architecture. If you want a batteries-included Node tool today,
use career-ops.

## License

MIT © 2026 RevREB. See [LICENSE](LICENSE).
