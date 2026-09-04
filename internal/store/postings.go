package store

import "context"

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
		`INSERT INTO applications (url, company, role)
		 VALUES ($1, $2, $3)
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
