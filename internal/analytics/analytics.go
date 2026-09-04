// Package analytics computes funnel/velocity metrics as SQL over the store.
package analytics

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Analytics runs read-only aggregate queries.
type Analytics struct {
	Pool *pgxpool.Pool
}

// New builds an Analytics over the given pool.
func New(pool *pgxpool.Pool) *Analytics { return &Analytics{Pool: pool} }

// Funnel returns the count of applications per status.
func (a *Analytics) Funnel(ctx context.Context) (map[string]int, error) {
	rows, err := a.Pool.Query(ctx,
		`SELECT status::text, count(*) FROM applications GROUP BY status`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var s string
		var n int
		if err := rows.Scan(&s, &n); err != nil {
			return nil, err
		}
		out[s] = n
	}
	return out, rows.Err()
}
