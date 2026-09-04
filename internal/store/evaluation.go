package store

import (
	"context"
	"encoding/json"
)

// SaveEvaluation records an LLM evaluation: sets the score, transitions the
// status, appends a status event, and stores the report document.
func (s *Store) SaveEvaluation(ctx context.Context, id int64, score float64, to string, report json.RawMessage) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var cur string
	if err := tx.QueryRow(ctx, `SELECT status::text FROM applications WHERE id=$1`, id).Scan(&cur); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`UPDATE applications SET score=$2, status=$3::application_status, updated_at=now() WHERE id=$1`,
		id, score, to); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO status_events (application_id, from_status, to_status, source)
		 VALUES ($1, $2::application_status, $3::application_status, 'evaluate')`, id, cur, to); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO reports (application_id, doc) VALUES ($1, $2)`, id, []byte(report)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
