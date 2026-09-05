package store

import "context"

// BackfillURLsFromReports recovers a URL for canonical apps that have none, by
// running extract() over the app's (longest) report markdown. Dry-run unless
// apply=true. Returns how many URLs are recoverable and how many were written.
func (s *Store) BackfillURLsFromReports(ctx context.Context, apply bool, extract func(md string) string) (candidates, updated int, err error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT a.id,
		       (SELECT r.doc->>'markdown' FROM reports r
		        WHERE r.application_id = a.id AND r.doc ? 'markdown'
		        ORDER BY length(r.doc->>'markdown') DESC LIMIT 1)
		FROM applications a
		WHERE a.canonical_id IS NULL AND coalesce(a.url,'') = ''
		  AND EXISTS (SELECT 1 FROM reports r WHERE r.application_id = a.id AND r.doc ? 'markdown')`)
	if err != nil {
		return 0, 0, err
	}
	type hit struct {
		id  int64
		url string
	}
	var hits []hit
	for rows.Next() {
		var id int64
		var md *string
		if err := rows.Scan(&id, &md); err != nil {
			rows.Close()
			return 0, 0, err
		}
		if md == nil {
			continue
		}
		if u := extract(*md); u != "" {
			hits = append(hits, hit{id, u})
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, 0, err
	}
	candidates = len(hits)
	if !apply {
		return candidates, 0, nil
	}
	for _, h := range hits {
		tag, e := s.Pool.Exec(ctx,
			`UPDATE applications SET url = $2, updated_at = now() WHERE id = $1 AND coalesce(url,'') = ''`, h.id, h.url)
		if e != nil {
			return candidates, updated, e
		}
		updated += int(tag.RowsAffected())
	}
	return candidates, updated, nil
}

// RequeueUnreported fixes the core defect: canonical apps marked 'evaluated' with
// NO report but a scraped posting are not really evaluated — reset them to inbox
// (score cleared) so the A–G consumer regenerates a real report. Dry-run unless
// apply=true. Returns how many were (or would be) requeued.
func (s *Store) RequeueUnreported(ctx context.Context, apply bool) (int, error) {
	const where = `
		WHERE a.canonical_id IS NULL AND a.status = 'evaluated'
		  AND EXISTS (SELECT 1 FROM postings p WHERE p.application_id = a.id)
		  AND NOT EXISTS (SELECT 1 FROM reports r WHERE r.application_id = a.id)`
	if !apply {
		var n int
		if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM applications a`+where).Scan(&n); err != nil {
			return 0, err
		}
		return n, nil
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT a.id FROM applications a`+where)
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
			`UPDATE applications SET status='inbox'::application_status, score=NULL, eval_claimed_at=NULL, updated_at=now() WHERE id=$1`, id); err != nil {
			return 0, err
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO status_events (application_id, from_status, to_status, source)
			 VALUES ($1, 'evaluated'::application_status, 'inbox'::application_status, 'remediate')`, id); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return len(ids), nil
}
