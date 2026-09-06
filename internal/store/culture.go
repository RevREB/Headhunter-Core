package store

import "context"

// CultureNote is one evaluator culture judgment: its verdict plus the free-text
// evidence the model cited (extracted from the report's "Culture screen" row).
type CultureNote struct {
	Company  string `json:"company"`
	Verdict  string `json:"verdict"`
	Evidence string `json:"evidence"`
}

// CultureNotes returns every caution/fail culture judgment (canonical records
// with an A–G summary), for empirical analysis of what the culture screen has
// actually been keying on.
func (s *Store) CultureNotes(ctx context.Context) ([]CultureNote, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT a.company,
		       r.doc->'summary'->'risk_summary'->>'culture' AS verdict,
		       btrim(substring(r.doc->>'markdown' from '\| Culture screen \| ([^|]+)')) AS evidence
		FROM applications a
		CROSS JOIN LATERAL (SELECT doc FROM reports rr WHERE rr.application_id=a.id ORDER BY rr.id DESC LIMIT 1) r
		WHERE a.canonical_id IS NULL
		  AND r.doc ? 'summary'
		  AND r.doc->'summary'->'risk_summary'->>'culture' IN ('caution','fail')`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CultureNote{}
	for rows.Next() {
		var n CultureNote
		var ev *string
		if err := rows.Scan(&n.Company, &n.Verdict, &ev); err != nil {
			return nil, err
		}
		if ev != nil {
			n.Evidence = *ev
		}
		out = append(out, n)
	}
	return out, rows.Err()
}
