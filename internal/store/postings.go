package store

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/RevREB/Headhunter-Core/internal/engine"
	"github.com/jackc/pgx/v5"
)

// nearDupMaxBits is the SimHash Hamming threshold for treating two postings that
// already share a dedup_key (normalized company+title) as the same role. Content
// near-identical (multi-location listing / repost) differs by only a few bits;
// two genuinely different roles that happen to share a title differ by many.
const nearDupMaxBits = 12

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
	Created       bool // false = exact-URL re-sighting of an existing application
	NearDup       bool // true = a new URL merged into an existing canonical role
}

// IngestPosting records a scraped posting. Dedup happens on two levels:
//   - exact: applications.url is UNIQUE, so the same URL re-sights the existing app.
//   - near: a new URL whose dedup_key (normalized company+title) matches an
//     existing canonical role and whose SimHash is within nearDupMaxBits is
//     recorded as a near-dup — a new row carrying canonical_id -> that canonical,
//     so multi-location listings and reposts collapse to one role in the worklist.
func (s *Store) IngestPosting(
	ctx context.Context,
	normURL, ats, company, role, dedupKey string,
	fingerprint uint64, trust int, rawDoc []byte,
) (IngestResult, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return IngestResult{}, err
	}
	defer tx.Rollback(ctx)

	fpBytes := int64ToBytes(int64(fingerprint))

	var appID int64
	created, nearDup := false, false

	if e := tx.QueryRow(ctx, `SELECT id FROM applications WHERE url=$1`, normURL).Scan(&appID); e == nil {
		// Exact URL already tracked: backfill dedup metadata if absent, refresh doc.
		if _, err := tx.Exec(ctx,
			`UPDATE applications SET dedup_key=coalesce(dedup_key,$2), fingerprint=coalesce(fingerprint,$3) WHERE id=$1`,
			appID, dedupKey, fpBytes); err != nil {
			return IngestResult{}, err
		}
		if tag, err := tx.Exec(ctx, `UPDATE postings SET doc=$2 WHERE application_id=$1`, appID, rawDoc); err != nil {
			return IngestResult{}, err
		} else if tag.RowsAffected() == 0 {
			if _, err := tx.Exec(ctx, `INSERT INTO postings (application_id, doc) VALUES ($1, $2)`, appID, rawDoc); err != nil {
				return IngestResult{}, err
			}
		}
	} else {
		// New URL: is it a near-dup of an existing canonical role?
		var canonicalID *int64
		if dedupKey != "" {
			rows, qerr := tx.Query(ctx,
				`SELECT id, fingerprint FROM applications
				 WHERE dedup_key=$1 AND canonical_id IS NULL AND status='inbox' AND fingerprint IS NOT NULL`, dedupKey)
			if qerr == nil {
				best := nearDupMaxBits + 1
				for rows.Next() {
					var cid int64
					var cfb []byte
					if rows.Scan(&cid, &cfb) == nil {
						if d := engine.Hamming(fingerprint, bytesToUint64(cfb)); d <= nearDupMaxBits && d < best {
							best, canonicalID = d, &cid
						}
					}
				}
				rows.Close()
			}
		}
		if canonicalID != nil {
			nearDup = true
		}
		if err := tx.QueryRow(ctx,
			`INSERT INTO applications (url, company, role, status, dedup_key, fingerprint, canonical_id)
			 VALUES ($1, $2, $3, 'inbox'::application_status, $4, $5, $6) RETURNING id`,
			normURL, company, role, dedupKey, fpBytes, canonicalID).Scan(&appID); err != nil {
			return IngestResult{}, err
		}
		created = true
		if _, err := tx.Exec(ctx, `INSERT INTO postings (application_id, doc) VALUES ($1, $2)`, appID, rawDoc); err != nil {
			return IngestResult{}, err
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO status_events (application_id, to_status, source)
			 VALUES ($1, 'inbox'::application_status, 'scan')`, appID); err != nil {
			return IngestResult{}, err
		}
	}

	// Always record the sighting (dedup/repost analytics live off this table).
	if _, err := tx.Exec(ctx,
		`INSERT INTO scan_sightings (application_id, url, ats, fingerprint, trust_score)
		 VALUES ($1, $2, $3, $4, $5)`,
		appID, normURL, ats, fpBytes, trust); err != nil {
		return IngestResult{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return IngestResult{}, err
	}
	return IngestResult{ApplicationID: appID, Created: created, NearDup: nearDup}, nil
}

func int64ToBytes(v int64) []byte {
	b := make([]byte, 8)
	for i := 0; i < 8; i++ {
		b[7-i] = byte(v >> (8 * i))
	}
	return b
}

func bytesToUint64(b []byte) uint64 {
	var v uint64
	for i := 0; i < len(b) && i < 8; i++ {
		v = (v << 8) | uint64(b[i])
	}
	return v
}
