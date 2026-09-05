package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// ClaimNextInbox atomically claims the next unclaimed inbox posting for
// evaluation (FOR UPDATE SKIP LOCKED so concurrent workers never collide) and
// stamps eval_claimed_at. Returns nil when the queue is empty.
func (s *Store) ClaimNextInbox(ctx context.Context) (*Application, error) {
	var a Application
	var score *float64
	err := s.Pool.QueryRow(ctx, `
		UPDATE applications SET eval_claimed_at = now()
		WHERE id = (
			SELECT id FROM applications
			WHERE status = 'inbox' AND canonical_id IS NULL AND eval_claimed_at IS NULL
			ORDER BY id
			LIMIT 1 FOR UPDATE SKIP LOCKED)
		RETURNING id, coalesce(url,''), company, role, score::float8, status::text, created_at`).
		Scan(&a.ID, &a.URL, &a.Company, &a.Role, &score, &a.Status, &a.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	a.Score = score
	return &a, nil
}

// ReleaseClaim clears a claim so the posting is retried (on eval failure).
func (s *Store) ReleaseClaim(ctx context.Context, id int64) error {
	_, err := s.Pool.Exec(ctx, `UPDATE applications SET eval_claimed_at = NULL WHERE id = $1`, id)
	return err
}

// ReclaimStaleClaims releases claims on inbox postings older than olderThanSecs
// (a crashed worker's in-flight rows). Pass 0 on boot to release all inbox claims.
func (s *Store) ReclaimStaleClaims(ctx context.Context, olderThanSecs int) (int, error) {
	tag, err := s.Pool.Exec(ctx, `
		UPDATE applications SET eval_claimed_at = NULL
		WHERE status = 'inbox' AND eval_claimed_at IS NOT NULL
		  AND eval_claimed_at < now() - make_interval(secs => $1)`, olderThanSecs)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

// EnqueueForReeval re-queues scraped evaluated apps (those with a JD) back to
// inbox so the consumer re-scores them. Imported apps (no posting) are left
// alone — they have no JD to evaluate. Returns the count re-queued.
func (s *Store) EnqueueForReeval(ctx context.Context) (int, error) {
	tag, err := s.Pool.Exec(ctx, `
		UPDATE applications SET status = 'inbox', eval_claimed_at = NULL
		WHERE status = 'evaluated' AND canonical_id IS NULL
		  AND EXISTS (SELECT 1 FROM postings p WHERE p.application_id = applications.id)`)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

// QueueStats returns the inbox queue depth: waiting (unclaimed) and inFlight
// (claimed) canonical postings.
func (s *Store) QueueStats(ctx context.Context) (waiting, inFlight int, err error) {
	err = s.Pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE eval_claimed_at IS NULL),
		       count(*) FILTER (WHERE eval_claimed_at IS NOT NULL)
		FROM applications WHERE status = 'inbox' AND canonical_id IS NULL`).Scan(&waiting, &inFlight)
	return
}

// ClaimedRole is an inbox posting currently claimed for evaluation (in flight).
type ClaimedRole struct {
	ID      int64  `json:"id"`
	Company string `json:"company"`
	Role    string `json:"role"`
}

// ClaimedRoles lists the inbox postings currently being evaluated (claimed,
// oldest claim first), so the UI can show which roles are in flight right now.
func (s *Store) ClaimedRoles(ctx context.Context) ([]ClaimedRole, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT id, company, role FROM applications
		WHERE status = 'inbox' AND canonical_id IS NULL AND eval_claimed_at IS NOT NULL
		ORDER BY eval_claimed_at
		LIMIT 20`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ClaimedRole{}
	for rows.Next() {
		var c ClaimedRole
		if err := rows.Scan(&c.ID, &c.Company, &c.Role); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
