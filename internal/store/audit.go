package store

import "context"

// IntegrityReport quantifies data-shape defects across canonical applications —
// the forensic basis for a remediation, and the way to verify one afterward.
type IntegrityReport struct {
	Total    int            `json:"total"`
	ByStatus map[string]int `json:"byStatus"`
	Shapes   struct {
		URLEmpty           int `json:"urlEmpty"`
		PostingMissing     int `json:"postingMissing"`
		ReportMissing      int `json:"reportMissing"`
		ReportAG           int `json:"reportAG"`           // report carries a machine summary
		ReportMarkdownOnly int `json:"reportMarkdownOnly"` // imported career-ops report (no summary)
	} `json:"shapes"`
	Defects struct {
		EvaluatedNoReport      int `json:"evaluatedNoReport"`      // status=evaluated but no report row
		EvaluatedURLEmpty      int `json:"evaluatedUrlEmpty"`      // status=evaluated but no URL
		EvaluatedPostingMissing int `json:"evaluatedPostingMissing"`
		ScoredNoReport         int `json:"scoredNoReport"`         // has a score but no report
		URLEmptyButInReport    int `json:"urlEmptyButInReport"`    // url empty yet a URL is recoverable from the report
	} `json:"defects"`
}

// AuditIntegrity computes the integrity report over canonical applications.
func (s *Store) AuditIntegrity(ctx context.Context) (*IntegrityReport, error) {
	rep := &IntegrityReport{ByStatus: map[string]int{}}

	rows, err := s.Pool.Query(ctx,
		`SELECT status::text, count(*) FROM applications WHERE canonical_id IS NULL GROUP BY status`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var st string
		var n int
		if err := rows.Scan(&st, &n); err != nil {
			rows.Close()
			return nil, err
		}
		rep.ByStatus[st] = n
		rep.Total += n
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	err = s.Pool.QueryRow(ctx, `
		SELECT
		  count(*) FILTER (WHERE coalesce(a.url,'')=''),
		  count(*) FILTER (WHERE NOT EXISTS (SELECT 1 FROM postings p WHERE p.application_id=a.id)),
		  count(*) FILTER (WHERE NOT EXISTS (SELECT 1 FROM reports r WHERE r.application_id=a.id)),
		  count(*) FILTER (WHERE EXISTS (SELECT 1 FROM reports r WHERE r.application_id=a.id AND (r.doc ? 'summary'))),
		  count(*) FILTER (WHERE EXISTS (SELECT 1 FROM reports r WHERE r.application_id=a.id AND NOT (r.doc ? 'summary'))),
		  count(*) FILTER (WHERE a.status='evaluated' AND NOT EXISTS (SELECT 1 FROM reports r WHERE r.application_id=a.id)),
		  count(*) FILTER (WHERE a.status='evaluated' AND coalesce(a.url,'')=''),
		  count(*) FILTER (WHERE a.status='evaluated' AND NOT EXISTS (SELECT 1 FROM postings p WHERE p.application_id=a.id)),
		  count(*) FILTER (WHERE a.score IS NOT NULL AND NOT EXISTS (SELECT 1 FROM reports r WHERE r.application_id=a.id)),
		  count(*) FILTER (WHERE coalesce(a.url,'')='' AND EXISTS (
		        SELECT 1 FROM reports r WHERE r.application_id=a.id
		          AND r.doc->>'markdown' ~ 'https?://[^ )]+'))
		FROM applications a WHERE a.canonical_id IS NULL`).
		Scan(&rep.Shapes.URLEmpty, &rep.Shapes.PostingMissing, &rep.Shapes.ReportMissing,
			&rep.Shapes.ReportAG, &rep.Shapes.ReportMarkdownOnly,
			&rep.Defects.EvaluatedNoReport, &rep.Defects.EvaluatedURLEmpty, &rep.Defects.EvaluatedPostingMissing,
			&rep.Defects.ScoredNoReport, &rep.Defects.URLEmptyButInReport)
	if err != nil {
		return nil, err
	}
	return rep, nil
}
