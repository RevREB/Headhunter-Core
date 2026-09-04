package store

import (
	"context"
	"encoding/json"
	"sort"

	"github.com/RevREB/Headhunter-Core/internal/engine"
)

// DedupCluster is one canonical role and the near-duplicate rows folded into it.
type DedupCluster struct {
	CanonicalID int64   `json:"canonicalId"`
	Company     string  `json:"company"`
	Role        string  `json:"role"`
	DupCount    int     `json:"dupCount"`
	DupIDs      []int64 `json:"dupIds"`
}

// DedupReport summarizes a DedupInbox pass.
type DedupReport struct {
	Scanned  int            `json:"scanned"`  // inbox canonical rows examined
	Clusters int            `json:"clusters"` // roles with >=1 near-dup
	Merged   int            `json:"merged"`   // near-dup rows folded away
	Applied  bool           `json:"applied"`  // false = dry run
	Examples []DedupCluster `json:"examples"` // largest clusters first
}

// DedupInbox collapses existing inbox near-duplicates: rows sharing a dedup_key
// (normalized company+title) whose SimHash is within maxBits are folded into the
// lowest-id canonical (canonical_id set; nothing deleted). apply=false is a dry
// run that reports what would merge without writing. Also backfills dedup_key /
// fingerprint on the scanned rows when applying.
func (s *Store) DedupInbox(ctx context.Context, apply bool, maxBits int) (*DedupReport, error) {
	if maxBits <= 0 {
		maxBits = nearDupMaxBits
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT a.id, a.company, a.role,
		       (SELECT p.doc FROM postings p WHERE p.application_id=a.id ORDER BY p.id DESC LIMIT 1)
		FROM applications a
		WHERE a.status='inbox' AND a.canonical_id IS NULL
		ORDER BY a.id ASC`)
	if err != nil {
		return nil, err
	}
	type app struct {
		id            int64
		company, role string
		key           string
		fp            uint64
		hasFp         bool
	}
	groups := map[string][]app{}
	scanned := 0
	for rows.Next() {
		var a app
		var doc []byte
		if err := rows.Scan(&a.id, &a.company, &a.role, &doc); err != nil {
			rows.Close()
			return nil, err
		}
		a.key = engine.DedupKey(a.company, a.role)
		if len(doc) > 0 {
			// Fingerprint the posting content the same way ingest does, so the
			// one-time collapse and future ingests agree.
			var rp struct {
				Title string          `json:"title"`
				Raw   json.RawMessage `json:"raw"`
			}
			if json.Unmarshal(doc, &rp) == nil {
				a.fp, a.hasFp = engine.ContentFingerprint(rp.Title, rp.Raw), true
			}
		}
		groups[a.key] = append(groups[a.key], a)
		scanned++
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rep := &DedupReport{Scanned: scanned, Applied: apply}
	assign := map[int64]int64{} // dup id -> canonical id
	for _, g := range groups {
		if len(g) < 2 {
			continue
		}
		var canon *app
		for i := range g {
			if g[i].hasFp {
				canon = &g[i]
				break
			}
		}
		if canon == nil {
			continue
		}
		var dupIDs []int64
		for i := range g {
			a := g[i]
			if a.id == canon.id || !a.hasFp {
				continue
			}
			if engine.Hamming(canon.fp, a.fp) <= maxBits {
				assign[a.id] = canon.id
				dupIDs = append(dupIDs, a.id)
			}
		}
		if len(dupIDs) > 0 {
			rep.Clusters++
			rep.Merged += len(dupIDs)
			rep.Examples = append(rep.Examples, DedupCluster{
				CanonicalID: canon.id, Company: canon.company, Role: canon.role,
				DupCount: len(dupIDs), DupIDs: dupIDs,
			})
		}
	}
	sort.Slice(rep.Examples, func(i, j int) bool { return rep.Examples[i].DupCount > rep.Examples[j].DupCount })
	if len(rep.Examples) > 15 {
		rep.Examples = rep.Examples[:15]
	}

	if apply && len(assign) > 0 {
		tx, err := s.Pool.Begin(ctx)
		if err != nil {
			return rep, err
		}
		defer tx.Rollback(ctx)
		// Backfill dedup_key/fingerprint on every scanned row (so future ingests
		// and the near-dup path can match against them).
		for _, g := range groups {
			for _, a := range g {
				if a.hasFp {
					if _, err := tx.Exec(ctx, `UPDATE applications SET dedup_key=$2, fingerprint=$3 WHERE id=$1`,
						a.id, a.key, int64ToBytes(int64(a.fp))); err != nil {
						return rep, err
					}
				} else if _, err := tx.Exec(ctx, `UPDATE applications SET dedup_key=$2 WHERE id=$1`, a.id, a.key); err != nil {
					return rep, err
				}
			}
		}
		for dupID, canonID := range assign {
			if _, err := tx.Exec(ctx, `UPDATE applications SET canonical_id=$2 WHERE id=$1`, dupID, canonID); err != nil {
				return rep, err
			}
		}
		if err := tx.Commit(ctx); err != nil {
			return rep, err
		}
	}
	return rep, nil
}
