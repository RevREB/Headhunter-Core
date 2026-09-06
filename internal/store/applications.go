package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// ErrNotFound is returned when a requested application does not exist.
var ErrNotFound = errors.New("application not found")

// Application is a row of the tracker.
type Application struct {
	ID        int64     `json:"id"`
	URL       string    `json:"url"`
	Company   string    `json:"company"`
	Role      string    `json:"role"`
	Score     *float64  `json:"score"`
	Status    string    `json:"status"`
	Imported  bool      `json:"imported"` // career-ops legacy import (no scraped posting)
	CreatedAt time.Time `json:"createdAt"`
}

// StatusEvent is one entry in an application's transition ledger.
type StatusEvent struct {
	From   *string   `json:"from"`
	To     string    `json:"to"`
	Source string    `json:"source"`
	Note   string    `json:"note"`
	At     time.Time `json:"at"`
}

// ApplicationDetail is the full record for one application: its row plus the
// latest raw posting, the latest evaluation report, and its status history.
type ApplicationDetail struct {
	Application
	UpdatedAt     time.Time       `json:"updatedAt"`
	DisappearedAt *time.Time      `json:"disappeared_at"` // set when the listing was detected gone
	Posting       json.RawMessage `json:"posting"`        // raw scraped posting doc, or null
	Report        json.RawMessage `json:"report"`         // latest eval report, or null
	Events        []StatusEvent   `json:"events"`
}

// GetApplication returns the full record for one application. Returns
// ErrNotFound when the id does not exist. Posting/Report are nil when absent.
func (s *Store) GetApplication(ctx context.Context, id int64) (*ApplicationDetail, error) {
	var d ApplicationDetail
	var score *float64
	err := s.Pool.QueryRow(ctx,
		`SELECT id, coalesce(url,''), company, role, score::float8, status::text, imported, created_at, updated_at, disappeared_at
		 FROM applications WHERE id=$1`, id).
		Scan(&d.ID, &d.URL, &d.Company, &d.Role, &score, &d.Status, &d.Imported, &d.CreatedAt, &d.UpdatedAt, &d.DisappearedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	d.Score = score

	// latest posting + report (both optional)
	var posting, report []byte
	if err := s.Pool.QueryRow(ctx,
		`SELECT doc FROM postings WHERE application_id=$1 ORDER BY id DESC LIMIT 1`, id).Scan(&posting); err == nil {
		d.Posting = posting
	}
	if err := s.Pool.QueryRow(ctx,
		`SELECT doc FROM reports WHERE application_id=$1 ORDER BY id DESC LIMIT 1`, id).Scan(&report); err == nil {
		d.Report = report
	}

	// status history (oldest first)
	rows, err := s.Pool.Query(ctx,
		`SELECT from_status::text, to_status::text, coalesce(source,''), coalesce(note,''), at
		 FROM status_events WHERE application_id=$1 ORDER BY at ASC, id ASC`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	d.Events = []StatusEvent{}
	for rows.Next() {
		var ev StatusEvent
		if err := rows.Scan(&ev.From, &ev.To, &ev.Source, &ev.Note, &ev.At); err != nil {
			return nil, err
		}
		d.Events = append(d.Events, ev)
	}
	return &d, rows.Err()
}

// RoleRow is one row returned to the read-only MCP list_roles tool.
type RoleRow struct {
	ID          int64      `json:"id"`
	Company     string     `json:"company"`
	Role        string     `json:"role"`
	Score       *float64   `json:"score"`
	Status      string     `json:"status"`
	URL         string     `json:"url"`
	EvaluatedAt *time.Time `json:"evaluated_at"`
}

// RolesForMCP powers the MCP list_roles tool: canonical-deduped roles filtered
// by status, minimum score, and evaluated-since, highest fit first. minScore < 0
// disables the score filter; empty status = any status; nil since = no time
// filter. evaluated_at is the most recent transition into 'evaluated'.
func (s *Store) RolesForMCP(ctx context.Context, status string, minScore float64, since *time.Time, limit int) ([]RoleRow, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	q := fmt.Sprintf(`
		SELECT a.id, a.company, a.role, a.score::float8, a.status::text, coalesce(a.url,''), ev.at
		FROM applications a
		LEFT JOIN LATERAL (
			SELECT max(at) AS at FROM status_events se
			WHERE se.application_id = a.id AND se.to_status = 'evaluated'
		) ev ON true
		WHERE a.canonical_id IS NULL
		  AND ($1 = '' OR a.status = $1::application_status)
		  AND ($2 < 0 OR a.score >= $2)
		  AND ($3::timestamptz IS NULL OR ev.at >= $3)
		ORDER BY a.score DESC NULLS LAST, a.id DESC
		LIMIT %d`, limit)
	rows, err := s.Pool.Query(ctx, q, status, minScore, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RoleRow{}
	for rows.Next() {
		var r RoleRow
		if err := rows.Scan(&r.ID, &r.Company, &r.Role, &r.Score, &r.Status, &r.URL, &r.EvaluatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ListApplications returns applications newest-first, optionally filtered by
// status. limit is clamped to [1,5000].
func (s *Store) ListApplications(ctx context.Context, limit int, status string) ([]Application, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 20000 {
		limit = 20000 // cap, but large enough to sweep the whole backlog
	}
	// canonical_id IS NULL hides near-duplicate rows (multi-location listings /
	// reposts collapsed into their canonical role).
	q := `SELECT id, coalesce(url,''), company, role, score::float8, status::text, imported, created_at FROM applications WHERE canonical_id IS NULL`
	args := []any{}
	if status != "" {
		q += ` AND status = $1::application_status`
		args = append(args, status)
	}
	q += fmt.Sprintf(` ORDER BY created_at DESC LIMIT %d`, limit)

	rows, err := s.Pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Application{}
	for rows.Next() {
		var a Application
		var score *float64
		if err := rows.Scan(&a.ID, &a.URL, &a.Company, &a.Role, &score, &a.Status, &a.Imported, &a.CreatedAt); err != nil {
			return nil, err
		}
		a.Score = score
		out = append(out, a)
	}
	return out, rows.Err()
}
