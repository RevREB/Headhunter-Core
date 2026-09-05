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
