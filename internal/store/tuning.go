package store

import (
	"context"
	"sort"
	"strconv"
)

// Bucket is a labeled count for a tuning breakdown.
type Bucket struct {
	Key   string `json:"key"`
	Count int    `json:"count"`
}

// TuningReport splits the "not applying" pile into the two learning signals:
// below-the-line (score < min — tune what gets scanned) and pass-overs (score >=
// min but declined — tune the evaluator).
type TuningReport struct {
	MinScore  float64 `json:"minScore"`
	BelowLine struct {
		Total        int      `json:"total"`
		Scraped      int      `json:"scraped"`  // scraped (scan-actionable) subset
		Analyzed     int      `json:"analyzed"` // below-line records with an A–G summary
		ByFactor     []Bucket `json:"byFactor"` // WHY they missed — the primary signal
		TopCompanies []Bucket `json:"topCompanies"`
	} `json:"belowLine"`
	PassOver struct {
		Total    int      `json:"total"`
		ByReason []Bucket `json:"byReason"`
	} `json:"passOver"`
}

// DiscardTuning classifies discarded canonical records against minScore.
func (s *Store) DiscardTuning(ctx context.Context, minScore float64) (*TuningReport, error) {
	rep := &TuningReport{MinScore: minScore}

	// Below-the-line totals (all + scraped subset, which reflects current scans).
	if err := s.Pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE score < $1),
		       count(*) FILTER (WHERE score < $1 AND NOT imported)
		FROM applications
		WHERE canonical_id IS NULL AND status='discarded' AND score IS NOT NULL`, minScore).
		Scan(&rep.BelowLine.Total, &rep.BelowLine.Scraped); err != nil {
		return nil, err
	}

	// Below-line by company — scraped only (imports aren't from current scans, so
	// they can't tune the scan filters).
	rep.BelowLine.TopCompanies = []Bucket{}
	rows, err := s.Pool.Query(ctx, `
		SELECT company, count(*) FROM applications
		WHERE canonical_id IS NULL AND status='discarded' AND NOT imported
		  AND score IS NOT NULL AND score < $1
		GROUP BY company ORDER BY count(*) DESC, company LIMIT 15`, minScore)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var b Bucket
		if err := rows.Scan(&b.Key, &b.Count); err != nil {
			rows.Close()
			return nil, err
		}
		rep.BelowLine.TopCompanies = append(rep.BelowLine.TopCompanies, b)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// WHY below the line — the primary signal. Hard-stop reason codes (unnested
	// from the A–G summary) plus the soft comp/geo/culture misses. Latest report
	// per app; report-less below-line records simply aren't factor-analyzable.
	rep.BelowLine.ByFactor = []Bucket{}
	label := map[string]string{
		"onsite_required": "Onsite / wrong location", "heavy_coding_role": "Heavy coding role",
		"dba_ownership": "DBA / database ownership", "security_below_director": "Security below director",
		"casino_employer": "Casino employer", "ruled_out_company": "Ruled-out company",
		"work_auth_blocked": "Work-auth blocked",
	}
	hs, err := s.Pool.Query(ctx, `
		SELECT hs->>'reason_code' AS k, count(*)
		FROM applications a
		CROSS JOIN LATERAL (SELECT doc FROM reports rr WHERE rr.application_id=a.id ORDER BY rr.id DESC LIMIT 1) r
		CROSS JOIN LATERAL jsonb_array_elements(coalesce(r.doc->'summary'->'hard_stops','[]'::jsonb)) hs
		WHERE a.canonical_id IS NULL AND a.status='discarded' AND a.score IS NOT NULL AND a.score < $1
		GROUP BY 1 ORDER BY count(*) DESC`, minScore)
	if err != nil {
		return nil, err
	}
	for hs.Next() {
		var b Bucket
		if err := hs.Scan(&b.Key, &b.Count); err != nil {
			hs.Close()
			return nil, err
		}
		if l, ok := label[b.Key]; ok {
			b.Key = l
		}
		rep.BelowLine.ByFactor = append(rep.BelowLine.ByFactor, b)
	}
	hs.Close()
	if err := hs.Err(); err != nil {
		return nil, err
	}
	// Soft misses from risk_summary (latest report per app; LEFT JOIN so report-less
	// records still count toward the below-line denominator).
	var analyzed, compBelow, geoMismatch, culture int
	if err := s.Pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE r.doc ? 'summary'),
		       count(*) FILTER (WHERE r.doc->'summary'->'risk_summary'->>'comp_vs_target'='below'),
		       count(*) FILTER (WHERE r.doc->'summary'->'risk_summary'->>'geo'='mismatch'),
		       count(*) FILTER (WHERE r.doc->'summary'->'risk_summary'->>'culture' IN ('fail','caution'))
		FROM applications a
		LEFT JOIN LATERAL (SELECT doc FROM reports rr WHERE rr.application_id=a.id ORDER BY rr.id DESC LIMIT 1) r ON true
		WHERE a.canonical_id IS NULL AND a.status='discarded' AND a.score IS NOT NULL AND a.score < $1`, minScore).
		Scan(&analyzed, &compBelow, &geoMismatch, &culture); err != nil {
		return nil, err
	}
	rep.BelowLine.Analyzed = analyzed
	for _, sm := range []struct {
		k string
		n int
	}{{"Comp below target", compBelow}, {"Geo mismatch", geoMismatch}, {"Culture concern", culture}} {
		if sm.n > 0 {
			rep.BelowLine.ByFactor = append(rep.BelowLine.ByFactor, Bucket{Key: sm.k, Count: sm.n})
		}
	}
	sort.SliceStable(rep.BelowLine.ByFactor, func(i, j int) bool {
		return rep.BelowLine.ByFactor[i].Count > rep.BelowLine.ByFactor[j].Count
	})

	// Pass-overs: above the line but discarded, grouped by the human's reason.
	if err := s.Pool.QueryRow(ctx, `
		SELECT count(*) FROM applications
		WHERE canonical_id IS NULL AND status='discarded' AND score IS NOT NULL AND score >= $1`, minScore).
		Scan(&rep.PassOver.Total); err != nil {
		return nil, err
	}
	rep.PassOver.ByReason = []Bucket{}
	rrows, err := s.Pool.Query(ctx, `
		SELECT coalesce(nullif(disposition_reason,''), 'unspecified'), count(*)
		FROM applications
		WHERE canonical_id IS NULL AND status='discarded' AND score IS NOT NULL AND score >= $1
		GROUP BY 1 ORDER BY count(*) DESC`, minScore)
	if err != nil {
		return nil, err
	}
	for rrows.Next() {
		var b Bucket
		if err := rrows.Scan(&b.Key, &b.Count); err != nil {
			rrows.Close()
			return nil, err
		}
		rep.PassOver.ByReason = append(rep.PassOver.ByReason, b)
	}
	rrows.Close()
	return rep, rrows.Err()
}

// DiscardBelowLine moves canonical 'evaluated' records scoring below minScore to
// 'discarded' with reason 'below_bar' (a human-invoked bulk classification — the
// system never does this on its own). Dry-run unless apply=true. Returns count.
func (s *Store) DiscardBelowLine(ctx context.Context, minScore float64, apply bool) (int, error) {
	const where = `WHERE canonical_id IS NULL AND status='evaluated' AND score IS NOT NULL AND score < $1`
	if !apply {
		var n int
		if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM applications `+where, minScore).Scan(&n); err != nil {
			return 0, err
		}
		return n, nil
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT id FROM applications `+where, minScore)
	if err != nil {
		return 0, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for _, id := range ids {
		if _, err := tx.Exec(ctx,
			`UPDATE applications SET status='discarded'::application_status, disposition_reason='below_bar', updated_at=now() WHERE id=$1`, id); err != nil {
			return 0, err
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO status_events (application_id, from_status, to_status, source, note, at)
			 VALUES ($1, 'evaluated'::application_status, 'discarded'::application_status, 'tuning', 'below_bar', now())`, id); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return len(ids), nil
}

// ReqRow is a requeued below-line record with the score it had before re-eval.
type ReqRow struct {
	ID       int64   `json:"id"`
	OldScore float64 `json:"oldScore"`
}

// RequeueBelowLine sends below_bar discards that have a scraped posting back to
// inbox for re-evaluation under the current rubric, recording the prior score so
// the lift can be measured. limit<=0 = all. Dry-run unless apply=true.
func (s *Store) RequeueBelowLine(ctx context.Context, limit int, apply bool) ([]ReqRow, error) {
	where := `WHERE a.canonical_id IS NULL AND a.status='discarded' AND a.disposition_reason='below_bar'
	          AND a.score IS NOT NULL AND EXISTS (SELECT 1 FROM postings p WHERE p.application_id=a.id)`
	lim := ""
	if limit > 0 {
		lim = " LIMIT " + strconv.Itoa(limit)
	}
	rows, err := s.Pool.Query(ctx, `SELECT a.id, a.score::float8 FROM applications a `+where+` ORDER BY a.id`+lim)
	if err != nil {
		return nil, err
	}
	var out []ReqRow
	for rows.Next() {
		var r ReqRow
		if err := rows.Scan(&r.ID, &r.OldScore); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if !apply {
		return out, nil
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	for _, r := range out {
		if _, err := tx.Exec(ctx,
			`UPDATE applications SET status='inbox'::application_status, score=NULL, eval_claimed_at=NULL, disposition_reason=NULL, updated_at=now() WHERE id=$1`, r.ID); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO status_events (application_id, from_status, to_status, source, note, at)
			 VALUES ($1,'discarded'::application_status,'inbox'::application_status,'reeval',$2,now())`,
			r.ID, "below_bar re-test; prior score "+strconv.FormatFloat(r.OldScore, 'f', 1, 64)); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return out, nil
}
