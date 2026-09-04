package store

import (
	"context"
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

// ListApplications returns the most recent applications, newest first.
func (s *Store) ListApplications(ctx context.Context, limit int) ([]Application, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.Pool.Query(ctx,
		`SELECT id, coalesce(url,''), company, role, score::float8, status::text, created_at
		 FROM applications ORDER BY created_at DESC LIMIT $1`, limit)
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
