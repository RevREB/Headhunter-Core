package store

import (
	"context"
	"encoding/json"
)

// GetAllConfig returns every config row as key -> raw JSON document.
func (s *Store) GetAllConfig(ctx context.Context) (map[string]json.RawMessage, error) {
	rows, err := s.Pool.Query(ctx, `SELECT key, doc FROM config`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]json.RawMessage{}
	for rows.Next() {
		var k string
		var doc []byte
		if err := rows.Scan(&k, &doc); err != nil {
			return nil, err
		}
		out[k] = json.RawMessage(doc)
	}
	return out, rows.Err()
}

// SetConfig upserts a config document.
func (s *Store) SetConfig(ctx context.Context, key string, doc json.RawMessage) error {
	_, err := s.Pool.Exec(ctx,
		`INSERT INTO config (key, doc) VALUES ($1, $2)
		 ON CONFLICT (key) DO UPDATE SET doc = $2, updated_at = now()`, key, []byte(doc))
	return err
}
