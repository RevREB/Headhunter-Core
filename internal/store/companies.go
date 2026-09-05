package store

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/RevREB/Headhunter-Core/internal/engine"
	"github.com/jackc/pgx/v5"
)

// Company is a first-class company entity.
type Company struct {
	ID   int64  `json:"id"`
	Norm string `json:"norm"`
	Name string `json:"name"`
}

// upsertCompanyTx resolves (creating if needed) the company for a raw name
// inside an existing transaction, returning its id. Norm uses NormalizeCompany
// so ingest and backfill agree on the key. Returns 0 for an empty name.
func upsertCompanyTx(ctx context.Context, tx pgx.Tx, name string) (int64, error) {
	norm := engine.NormalizeCompany(name)
	if norm == "" {
		return 0, nil
	}
	var id int64
	err := tx.QueryRow(ctx, `
		INSERT INTO companies (norm, name) VALUES ($1, $2)
		ON CONFLICT (norm) DO UPDATE SET name = coalesce(nullif(companies.name, ''), EXCLUDED.name)
		RETURNING id`, norm, name).Scan(&id)
	return id, err
}

// BackfillCompanies creates a company row for every distinct existing
// application company and links applications.company_id. Idempotent: it only
// touches rows not yet linked, so it's cheap to run on every boot.
func (s *Store) BackfillCompanies(ctx context.Context) (int, error) {
	rows, err := s.Pool.Query(ctx,
		`SELECT DISTINCT company FROM applications
		 WHERE company_id IS NULL AND company IS NOT NULL AND btrim(company) <> ''`)
	if err != nil {
		return 0, err
	}
	var names []string
	for rows.Next() {
		var n string
		if rows.Scan(&n) == nil {
			names = append(names, n)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	linked := 0
	for _, name := range names {
		norm := engine.NormalizeCompany(name)
		if norm == "" {
			continue
		}
		var id int64
		if err := s.Pool.QueryRow(ctx, `
			INSERT INTO companies (norm, name) VALUES ($1, $2)
			ON CONFLICT (norm) DO UPDATE SET name = coalesce(nullif(companies.name, ''), EXCLUDED.name)
			RETURNING id`, norm, name).Scan(&id); err != nil {
			continue
		}
		// Link every application with this exact raw company string.
		tag, err := s.Pool.Exec(ctx,
			`UPDATE applications SET company_id=$1 WHERE company=$2 AND company_id IS NULL`, id, name)
		if err == nil {
			linked += int(tag.RowsAffected())
		}
	}
	return linked, nil
}

// ---- profiler work-queue (mirrors the eval queue) ----

// ClaimNextUnprofiledCompany atomically claims the next company that has never
// been profiled (or whose profile is older than ttlSecs), stamping
// profile_claimed_at. Returns nil when the queue is empty. Pass ttlSecs=0 to
// only claim never-profiled companies.
func (s *Store) ClaimNextUnprofiledCompany(ctx context.Context, ttlSecs int) (*Company, error) {
	var c Company
	err := s.Pool.QueryRow(ctx, `
		UPDATE companies SET profile_claimed_at = now()
		WHERE id = (
			SELECT id FROM companies
			WHERE profile_claimed_at IS NULL
			  AND (profiled_at IS NULL OR ($1 > 0 AND profiled_at < now() - make_interval(secs => $1)))
			ORDER BY profiled_at NULLS FIRST, id
			LIMIT 1 FOR UPDATE SKIP LOCKED)
		RETURNING id, norm, name`, ttlSecs).Scan(&c.ID, &c.Norm, &c.Name)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &c, nil
}

// ReleaseCompanyClaim clears a claim without marking profiled (on failure).
func (s *Store) ReleaseCompanyClaim(ctx context.Context, id int64) error {
	_, err := s.Pool.Exec(ctx, `UPDATE companies SET profile_claimed_at = NULL WHERE id = $1`, id)
	return err
}

// ReclaimStaleCompanyClaims releases claims older than olderThanSecs (0 = all),
// e.g. on boot after a restart.
func (s *Store) ReclaimStaleCompanyClaims(ctx context.Context, olderThanSecs int) (int, error) {
	tag, err := s.Pool.Exec(ctx, `
		UPDATE companies SET profile_claimed_at = NULL
		WHERE profile_claimed_at IS NOT NULL
		  AND profile_claimed_at < now() - make_interval(secs => $1)`, olderThanSecs)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

// CompanyQueueStats returns unprofiled/unclaimed vs claimed counts.
func (s *Store) CompanyQueueStats(ctx context.Context) (waiting, inFlight int, err error) {
	err = s.Pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE profiled_at IS NULL AND profile_claimed_at IS NULL),
		       count(*) FILTER (WHERE profile_claimed_at IS NOT NULL)
		FROM companies`).Scan(&waiting, &inFlight)
	return
}

// ReprofileAll requeues every company (sets profiled_at = NULL). Returns count.
func (s *Store) ReprofileAll(ctx context.Context) (int, error) {
	tag, err := s.Pool.Exec(ctx, `UPDATE companies SET profiled_at = NULL, profile_claimed_at = NULL`)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

// SampleCompanyDocs returns up to n raw posting docs for a company, so an
// enricher can mine them (e.g. Built In's hiringOrganization.sameAs, apply host).
func (s *Store) SampleCompanyDocs(ctx context.Context, companyID int64, n int) ([]json.RawMessage, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT p.doc FROM postings p
		JOIN applications a ON a.id = p.application_id
		WHERE a.company_id = $1
		ORDER BY a.id DESC
		LIMIT $2`, companyID, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []json.RawMessage
	for rows.Next() {
		var d json.RawMessage
		if rows.Scan(&d) == nil {
			out = append(out, d)
		}
	}
	return out, rows.Err()
}

// SaveCompanyProfile stores the assembled profile, discovered domain, and flags,
// and marks the company profiled.
func (s *Store) SaveCompanyProfile(ctx context.Context, id int64, profile []byte, domain string, flags []string) error {
	if flags == nil {
		flags = []string{}
	}
	_, err := s.Pool.Exec(ctx, `
		UPDATE companies
		SET profile = $2, domain = nullif($3, ''), flags = $4,
		    profiled_at = now(), profile_claimed_at = NULL, updated_at = now()
		WHERE id = $1`, id, profile, domain, flags)
	return err
}

// ---- reads for the UI ----

// CompanyRow is a company list entry with rolled-up posting stats.
type CompanyRow struct {
	ID         int64    `json:"id"`
	Name       string   `json:"name"`
	Domain     string   `json:"domain,omitempty"`
	Flags      []string `json:"flags,omitempty"`
	Postings   int      `json:"postings"`
	AvgScore   *float64 `json:"avgScore,omitempty"`
	Profiled   bool     `json:"profiled"`
	Size       string   `json:"size,omitempty"`
	Stage      string   `json:"stage,omitempty"`
	ATS        string   `json:"ats,omitempty"`
}

// ListCompanies returns companies with posting counts and average score,
// most-postings first.
func (s *Store) ListCompanies(ctx context.Context) ([]CompanyRow, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT c.id, c.name, coalesce(c.domain,''), c.flags,
		       count(a.id) AS postings,
		       avg(a.score)::float8 AS avg_score,
		       (c.profiled_at IS NOT NULL) AS profiled,
		       coalesce(c.profile #>> '{fields,employees,value}', '') AS size,
		       coalesce(c.profile #>> '{fields,stage,value}', '') AS stage,
		       coalesce(c.profile #>> '{fields,ats,value}', '') AS ats
		FROM companies c
		LEFT JOIN applications a ON a.company_id = c.id
		GROUP BY c.id
		ORDER BY count(a.id) DESC, c.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CompanyRow{}
	for rows.Next() {
		var r CompanyRow
		var avg *float64
		if err := rows.Scan(&r.ID, &r.Name, &r.Domain, &r.Flags, &r.Postings, &avg, &r.Profiled, &r.Size, &r.Stage, &r.ATS); err != nil {
			return nil, err
		}
		r.AvgScore = avg
		out = append(out, r)
	}
	return out, rows.Err()
}

// CompanyDetail is a company profile plus its postings.
type CompanyDetail struct {
	ID       int64           `json:"id"`
	Name     string          `json:"name"`
	Norm     string          `json:"norm"`
	Domain   string          `json:"domain,omitempty"`
	Flags    []string        `json:"flags,omitempty"`
	Profiled bool            `json:"profiled"`
	Profile  json.RawMessage `json:"profile"`
	Postings []CompanyPosting `json:"postings"`
}

// CompanyPosting is one posting under a company (for the detail view).
type CompanyPosting struct {
	ID     int64    `json:"id"`
	Role   string   `json:"role"`
	Status string   `json:"status"`
	Score  *float64 `json:"score,omitempty"`
}

// GetCompany returns a company's profile and its postings.
func (s *Store) GetCompany(ctx context.Context, id int64) (*CompanyDetail, error) {
	var d CompanyDetail
	var profiledAt *string
	err := s.Pool.QueryRow(ctx, `
		SELECT id, name, norm, coalesce(domain,''), flags, profile,
		       to_char(profiled_at, 'YYYY-MM-DD"T"HH24:MI:SSZ')
		FROM companies WHERE id = $1`, id).
		Scan(&d.ID, &d.Name, &d.Norm, &d.Domain, &d.Flags, &d.Profile, &profiledAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	d.Profiled = profiledAt != nil
	rows, err := s.Pool.Query(ctx, `
		SELECT id, role, status::text, score::float8
		FROM applications WHERE company_id = $1 ORDER BY id DESC LIMIT 500`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	d.Postings = []CompanyPosting{}
	for rows.Next() {
		var p CompanyPosting
		if err := rows.Scan(&p.ID, &p.Role, &p.Status, &p.Score); err != nil {
			return nil, err
		}
		d.Postings = append(d.Postings, p)
	}
	return &d, rows.Err()
}

// ---- Phase 2/3: eval context, synthesis, graph ----

// CompanyContext is a company's profile as evaluator context.
type CompanyContext struct {
	Name    string          `json:"name"`
	Flags   []string        `json:"flags"`
	Profile json.RawMessage `json:"profile"`
}

// CompanyContextForApp returns the company profile + flags for an application,
// or nil when the application has no linked company.
func (s *Store) CompanyContextForApp(ctx context.Context, appID int64) (*CompanyContext, error) {
	var c CompanyContext
	err := s.Pool.QueryRow(ctx, `
		SELECT c.name, c.flags, c.profile
		FROM companies c JOIN applications a ON a.company_id = c.id
		WHERE a.id = $1`, appID).Scan(&c.Name, &c.Flags, &c.Profile)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &c, nil
}

// SetCompanySummary writes just the (LLM-synthesized) summary into a profile,
// leaving sourced fields and profiled_at untouched.
func (s *Store) SetCompanySummary(ctx context.Context, id int64, summary string) error {
	b, _ := json.Marshal(summary)
	_, err := s.Pool.Exec(ctx, `
		UPDATE companies
		SET profile = jsonb_set(coalesce(profile, '{}'::jsonb), '{summary}', $2::jsonb), updated_at = now()
		WHERE id = $1`, id, b)
	return err
}

// SimilarCompanies returns companies sharing an ATS or an industry with the
// given company (the graph edges, computed live), best average score first.
func (s *Store) SimilarCompanies(ctx context.Context, id int64, limit int) ([]CompanyRow, error) {
	rows, err := s.Pool.Query(ctx, `
		WITH me AS (
			SELECT coalesce(profile #>> '{fields,ats,value}', '') AS ats,
			       coalesce(profile #> '{fields,industry,value}', '[]'::jsonb) AS ind
			FROM companies WHERE id = $1)
		SELECT c.id, c.name, coalesce(c.domain,''), c.flags,
		       count(a.id) AS postings, avg(a.score)::float8 AS avg_score,
		       (c.profiled_at IS NOT NULL) AS profiled,
		       coalesce(c.profile #>> '{fields,employees,value}', '') AS size,
		       coalesce(c.profile #>> '{fields,stage,value}', '') AS stage,
		       coalesce(c.profile #>> '{fields,ats,value}', '') AS ats
		FROM companies c, me
		LEFT JOIN applications a ON a.company_id = c.id
		WHERE c.id <> $1 AND (
			(me.ats <> '' AND c.profile #>> '{fields,ats,value}' = me.ats)
			OR (jsonb_array_length(me.ind) > 0
			    AND c.profile #> '{fields,industry,value}' ?| (SELECT array_agg(v) FROM jsonb_array_elements_text(me.ind) v)))
		GROUP BY c.id, me.ats, me.ind
		ORDER BY avg_score DESC NULLS LAST, postings DESC
		LIMIT $2`, id, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CompanyRow{}
	for rows.Next() {
		var r CompanyRow
		var avg *float64
		if err := rows.Scan(&r.ID, &r.Name, &r.Domain, &r.Flags, &r.Postings, &avg, &r.Profiled, &r.Size, &r.Stage, &r.ATS); err != nil {
			return nil, err
		}
		r.AvgScore = avg
		out = append(out, r)
	}
	return out, rows.Err()
}

// ATSDiscovery is a company whose ATS was inferred from its postings — a
// candidate to add to the per-ATS scraper (library expansion).
type ATSDiscovery struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	ATS      string `json:"ats"`
	Host     string `json:"host,omitempty"`
	Postings int    `json:"postings"`
}

// ATSDiscoveries lists companies with a detected ATS, best-covered first, so the
// operator can grow the scraper catalog from what the aggregator surfaced.
func (s *Store) ATSDiscoveries(ctx context.Context) ([]ATSDiscovery, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT c.id, c.name,
		       c.profile #>> '{fields,ats,value}' AS ats,
		       coalesce(c.profile #>> '{fields,ats,detail}', '') AS host,
		       count(a.id) AS postings
		FROM companies c
		LEFT JOIN applications a ON a.company_id = c.id
		WHERE c.profile #>> '{fields,ats,value}' IS NOT NULL
		GROUP BY c.id
		ORDER BY (c.profile #>> '{fields,ats,value}'), count(a.id) DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ATSDiscovery{}
	for rows.Next() {
		var d ATSDiscovery
		if err := rows.Scan(&d.ID, &d.Name, &d.ATS, &d.Host, &d.Postings); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
