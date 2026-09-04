package store

import (
	"context"
	"fmt"
	"time"
)

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

// ListApplications returns applications newest-first, optionally filtered by
// status. limit is clamped to [1,5000].
func (s *Store) ListApplications(ctx context.Context, limit int, status string) ([]Application, error) {
	if limit <= 0 || limit > 5000 {
		limit = 100
	}
	q := `SELECT id, coalesce(url,''), company, role, score::float8, status::text, created_at FROM applications`
	args := []any{}
	if status != "" {
		q += ` WHERE status = $1::application_status`
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
