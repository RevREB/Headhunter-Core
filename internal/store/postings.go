package store

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
)

// GetPostingDoc returns the latest raw posting document for an application, or
// nil when none exists (e.g. imported tracker rows have no scraped posting).
func (s *Store) GetPostingDoc(ctx context.Context, id int64) (json.RawMessage, error) {
	var doc []byte
	err := s.Pool.QueryRow(ctx,
		`SELECT doc FROM postings WHERE application_id=$1 ORDER BY id DESC LIMIT 1`, id).Scan(&doc)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return doc, nil
}

// IngestResult reports what happened to one scraped posting.
type IngestResult struct {
	ApplicationID int64
	Created       bool // false = deduped against an existing sighting
}

// IngestPosting records a scraped posting. It dedups on the normalized URL: if a
// sighting already exists, it returns the existing application; otherwise it
// creates an application (status 'evaluated'), stores the raw posting document,
// and records a scan sighting. All in one transaction.
func (s *Store) IngestPosting(
	ctx context.Context,
	normURL, ats, company, role string,
	fingerprint uint64, trust int, rawDoc []byte,
) (IngestResult, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return IngestResult{}, err
	}
	defer tx.Rollback(ctx)

	var appID int64
	// applications.url is UNIQUE — insert-or-fetch.
	err = tx.QueryRow(ctx,
		`INSERT INTO applications (url, company, role, status)
		 VALUES ($1, $2, $3, 'inbox'::application_status)
		 ON CONFLICT (url) DO NOTHING
		 RETURNING id`, normURL, company, role).Scan(&appID)
	created := true
	if err != nil {
		// no row returned -> the URL already existed; fetch it.
		if err := tx.QueryRow(ctx,
			`SELECT id FROM applications WHERE url=$1`, normURL).Scan(&appID); err != nil {
			return IngestResult{}, err
		}
		created = false
	}

	if created {
		if _, err := tx.Exec(ctx,
			`INSERT INTO postings (application_id, doc) VALUES ($1, $2)`,
			appID, rawDoc); err != nil {
			return IngestResult{}, err
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO status_events (application_id, to_status, source)
			 VALUES ($1, 'inbox'::application_status, 'scan')`, appID); err != nil {
			return IngestResult{}, err
		}
	} else {
		// Re-sighting: refresh the stored posting doc so a later scrape backfills
		// richer data (e.g. a JD that greenhouse/workday didn't capture before).
		tag, err := tx.Exec(ctx, `UPDATE postings SET doc=$2 WHERE application_id=$1`, appID, rawDoc)
		if err != nil {
			return IngestResult{}, err
		}
		if tag.RowsAffected() == 0 {
			if _, err := tx.Exec(ctx, `INSERT INTO postings (application_id, doc) VALUES ($1, $2)`, appID, rawDoc); err != nil {
				return IngestResult{}, err
			}
		}
	}
	// always record the sighting (dedup/repost analytics live off this table)
	fp := int64(fingerprint) // store the 64-bit SimHash as bytea-compatible int
	if _, err := tx.Exec(ctx,
		`INSERT INTO scan_sightings (application_id, url, ats, fingerprint, trust_score)
		 VALUES ($1, $2, $3, $4, $5)`,
		appID, normURL, ats, int64ToBytes(fp), trust); err != nil {
		return IngestResult{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return IngestResult{}, err
	}
	return IngestResult{ApplicationID: appID, Created: created}, nil
}

func int64ToBytes(v int64) []byte {
	b := make([]byte, 8)
	for i := 0; i < 8; i++ {
		b[7-i] = byte(v >> (8 * i))
	}
	return b
}
