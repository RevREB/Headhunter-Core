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
	UpdatedAt time.Time       `json:"updatedAt"`
	Posting   json.RawMessage `json:"posting"` // raw scraped posting doc, or null
	Report    json.RawMessage `json:"report"`  // latest eval report, or null
	Events    []StatusEvent   `json:"events"`
}

// GetApplication returns the full record for one application. Returns
// ErrNotFound when the id does not exist. Posting/Report are nil when absent.
func (s *Store) GetApplication(ctx context.Context, id int64) (*ApplicationDetail, error) {
	var d ApplicationDetail
	var score *float64
	err := s.Pool.QueryRow(ctx,
		`SELECT id, coalesce(url,''), company, role, score::float8, status::text, created_at, updated_at
		 FROM applications WHERE id=$1`, id).
		Scan(&d.ID, &d.URL, &d.Company, &d.Role, &score, &d.Status, &d.CreatedAt, &d.UpdatedAt)
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

// ListApplications returns applications newest-first, optionally filtered by
// status. limit is clamped to [1,5000].
func (s *Store) ListApplications(ctx context.Context, limit int, status string) ([]Application, error) {
	if limit <= 0 || limit > 5000 {
		limit = 100
	}
	// canonical_id IS NULL hides near-duplicate rows (multi-location listings /
	// reposts collapsed into their canonical role).
	q := `SELECT id, coalesce(url,''), company, role, score::float8, status::text, created_at FROM applications WHERE canonical_id IS NULL`
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
		if err := rows.Scan(&a.ID, &a.URL, &a.Company, &a.Role, &score, &a.Status, &a.CreatedAt); err != nil {
			return nil, err
		}
		a.Score = score
		out = append(out, a)
	}
	return out, rows.Err()
}
