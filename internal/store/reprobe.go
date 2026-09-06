package store

import (
	"context"
	"fmt"
)

// RecordScanRun logs a per-source scrape event (scraper contract v1.1): one ATS,
// N postings ingested this batch. Ground truth for future source-scoped set-diff
// expiry and per-ATS analytics. No-op for an empty source (a pre-v1.1 scraper).
func (s *Store) RecordScanRun(ctx context.Context, ats string, postings int, ok bool) error {
	if ats == "" {
		return nil
	}
	_, err := s.Pool.Exec(ctx,
		`INSERT INTO scan_runs (ats, postings, ok) VALUES ($1, $2, $3)`, ats, postings, ok)
	return err
}

// ProbeTarget is a posting URL to re-probe for disappearance.
type ProbeTarget struct {
	ID     int64
	URL    string
	Status string
}

// ProbeResult classifies the outcome of a re-probe.
type ProbeResult int

const (
	ProbeAlive     ProbeResult = iota // 2xx/3xx — the listing is still up; reset the gone streak
	ProbeGone                         // definitive 404/410 — advance the gone streak
	ProbeAmbiguous                    // 403/429/5xx/timeout — inconclusive; touch only, don't judge
)

// NextToProbe returns up to limit canonical roles with a live URL and no
// disappeared_at yet, least-recently-probed first (NULLs, i.e. never probed,
// come first). Drives the re-probe loop's rolling sweep of the backlog.
func (s *Store) NextToProbe(ctx context.Context, limit int) ([]ProbeTarget, error) {
	if limit <= 0 {
		limit = 25
	}
	rows, err := s.Pool.Query(ctx, fmt.Sprintf(`
		SELECT id, url, status::text FROM applications
		WHERE url IS NOT NULL AND url <> '' AND disappeared_at IS NULL AND canonical_id IS NULL
		ORDER BY last_probed_at ASC NULLS FIRST, id ASC
		LIMIT %d`, limit))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ProbeTarget{}
	for rows.Next() {
		var t ProbeTarget
		if err := rows.Scan(&t.ID, &t.URL, &t.Status); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// isPassiveStatus reports whether a role is one we never engaged with or passed
// on — the set that transitions to 'expired' when its listing disappears. Active
// pipeline statuses (applied→hired) keep their status.
func isPassiveStatus(s string) bool {
	switch s {
	case "inbox", "evaluated", "rejected", "discarded", "skip":
		return true
	}
	return false
}

// RecordProbe applies a probe outcome. Alive resets the gone streak; ambiguous
// only touches last_probed_at. Gone advances the streak and, once it reaches
// threshold, stamps disappeared_at (for ALL statuses, so the days-listed stat is
// complete) and transitions passive roles to 'expired' with a status event.
// Returns whether this call expired the role.
func (s *Store) RecordProbe(ctx context.Context, id int64, res ProbeResult, threshold int) (expired bool, err error) {
	switch res {
	case ProbeAlive:
		_, err = s.Pool.Exec(ctx, `UPDATE applications SET gone_probes=0, last_probed_at=now() WHERE id=$1`, id)
		return false, err
	case ProbeAmbiguous:
		_, err = s.Pool.Exec(ctx, `UPDATE applications SET last_probed_at=now() WHERE id=$1`, id)
		return false, err
	}
	if threshold < 1 {
		threshold = 1
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)

	var probes int
	var status string
	if err := tx.QueryRow(ctx,
		`UPDATE applications SET gone_probes = gone_probes + 1, last_probed_at = now()
		 WHERE id=$1 RETURNING gone_probes, status::text`, id).Scan(&probes, &status); err != nil {
		return false, err
	}
	if probes < threshold {
		return false, tx.Commit(ctx)
	}
	passive := isPassiveStatus(status)
	newStatus := status
	if passive {
		newStatus = "expired"
	}
	if _, err := tx.Exec(ctx,
		`UPDATE applications SET disappeared_at = coalesce(disappeared_at, now()),
		        status = $2::application_status, updated_at = now() WHERE id=$1`,
		id, newStatus); err != nil {
		return false, err
	}
	if passive {
		if _, err := tx.Exec(ctx,
			`INSERT INTO status_events (application_id, from_status, to_status, source, note)
			 VALUES ($1, $2::application_status, 'expired'::application_status, 'reprobe', 'listing gone from board')`,
			id, status); err != nil {
			return false, err
		}
	}
	return passive, tx.Commit(ctx)
}

// CompanyDaysListed is a per-company "days on board" rollup for the analytics UI.
type CompanyDaysListed struct {
	Company    string   `json:"company"`
	GoneRoles  int      `json:"gone_roles"`
	OpenRoles  int      `json:"open_roles"`
	MedianDays *float64 `json:"median_days"`
	AvgDays    *float64 `json:"avg_days"`
}

// DaysListedByCompany computes, per company, how long postings stay live — days
// listed = disappeared_at − first-seen (min scan sighting). Companies with at
// least one gone role, most-gone first. A hiring-velocity / req-longevity proxy;
// "days listed", not "time to hire" (a delisting isn't proof of a close).
func (s *Store) DaysListedByCompany(ctx context.Context, limit int) ([]CompanyDaysListed, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.Pool.Query(ctx, fmt.Sprintf(`
		SELECT a.company,
		       count(*) FILTER (WHERE a.disappeared_at IS NOT NULL) AS gone_roles,
		       count(*) FILTER (WHERE a.disappeared_at IS NULL)     AS open_roles,
		       percentile_cont(0.5) WITHIN GROUP (
		           ORDER BY EXTRACT(EPOCH FROM (a.disappeared_at - fs.first))/86400.0)
		           FILTER (WHERE a.disappeared_at IS NOT NULL AND fs.first IS NOT NULL) AS median_days,
		       avg(EXTRACT(EPOCH FROM (a.disappeared_at - fs.first))/86400.0)
		           FILTER (WHERE a.disappeared_at IS NOT NULL AND fs.first IS NOT NULL) AS avg_days
		FROM applications a
		LEFT JOIN LATERAL (
		    SELECT min(first_seen) AS first FROM scan_sightings ss WHERE ss.application_id = a.id
		) fs ON true
		WHERE a.canonical_id IS NULL
		GROUP BY a.company
		HAVING count(*) FILTER (WHERE a.disappeared_at IS NOT NULL) > 0
		ORDER BY gone_roles DESC, a.company ASC
		LIMIT %d`, limit))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CompanyDaysListed{}
	for rows.Next() {
		var c CompanyDaysListed
		if err := rows.Scan(&c.Company, &c.GoneRoles, &c.OpenRoles, &c.MedianDays, &c.AvgDays); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
