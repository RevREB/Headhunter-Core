package store

import (
	"context"
	"fmt"
	"time"

	"github.com/RevREB/Headhunter-Core/internal/engine"
	"github.com/RevREB/Headhunter-Core/internal/importer"
)

// LoadTracker truncates the application tables and bulk-loads parsed tracker
// rows (each seeded with one status event). Full-reload semantics — the DB is a
// derived view of the tracker.
func (s *Store) LoadTracker(ctx context.Context, rows []importer.TrackerRow) (int, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx,
		`TRUNCATE applications, status_events, scan_sightings, postings, reports RESTART IDENTITY CASCADE`); err != nil {
		return 0, err
	}
	n := 0
	for _, r := range rows {
		var createdAt *time.Time
		if r.HasDate {
			d := r.Date
			createdAt = &d
		}
		var id int64
		if err := tx.QueryRow(ctx,
			`INSERT INTO applications (company, role, score, status, created_at)
			 VALUES ($1, $2, $3, $4::application_status, COALESCE($5::timestamptz, now()))
			 RETURNING id`,
			r.Company, r.Role, r.Score, r.Status, createdAt).Scan(&id); err != nil {
			return n, fmt.Errorf("insert %q/%q: %w", r.Company, r.Role, err)
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO status_events (application_id, to_status, source, at)
			 VALUES ($1, $2::application_status, 'import', COALESCE($3::timestamptz, now()))`,
			id, r.Status, createdAt); err != nil {
			return n, err
		}
		n++
	}
	if err := tx.Commit(ctx); err != nil {
		return n, err
	}
	return n, nil
}

// SetStatus transitions an application, validated by the engine state machine,
// and appends a status event.
func (s *Store) SetStatus(ctx context.Context, id int64, to string) error {
	var cur string
	if err := s.Pool.QueryRow(ctx, `SELECT status::text FROM applications WHERE id=$1`, id).Scan(&cur); err != nil {
		return err
	}
	if !engine.CanTransition(engine.Status(cur), engine.Status(to)) {
		return fmt.Errorf("illegal transition %s -> %s", cur, to)
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx,
		`UPDATE applications SET status=$2::application_status, updated_at=now() WHERE id=$1`, id, to); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO status_events (application_id, from_status, to_status, source, at)
		 VALUES ($1, $2::application_status, $3::application_status, 'ui', now())`, id, cur, to); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
