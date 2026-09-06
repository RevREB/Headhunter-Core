package store

import "context"

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
		Scraped      int      `json:"scraped"` // scraped (scan-actionable) subset
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
