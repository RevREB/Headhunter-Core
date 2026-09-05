package store

import (
	"context"
	"encoding/json"
)

// ImportedCandidate is an imported tracker app with no scraped posting and no
// report yet — eligible to receive a migrated career-ops report.
type ImportedCandidate struct {
	ID      int64
	Company string
	Role    string
}

// ImportedCandidates returns tracker-imported apps (canonical, no scraped
// posting, no report) that can still receive a migrated report.
func (s *Store) ImportedCandidates(ctx context.Context) ([]ImportedCandidate, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT a.id, a.company, a.role FROM applications a
		WHERE a.canonical_id IS NULL
		  AND NOT EXISTS (SELECT 1 FROM postings p WHERE p.application_id = a.id)
		  AND NOT EXISTS (SELECT 1 FROM reports  r WHERE r.application_id = a.id)
		ORDER BY a.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ImportedCandidate
	for rows.Next() {
		var c ImportedCandidate
		if err := rows.Scan(&c.ID, &c.Company, &c.Role); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// AttachReport stores a migrated career-ops report (markdown only) on an app,
// leaving its imported status/score untouched.
func (s *Store) AttachReport(ctx context.Context, id int64, markdown string) error {
	doc, _ := json.Marshal(map[string]string{"markdown": markdown})
	_, err := s.Pool.Exec(ctx, `INSERT INTO reports (application_id, doc) VALUES ($1, $2)`, id, doc)
	return err
}
