package api

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/RevREB/Headhunter-Core/internal/enrich"
	"github.com/RevREB/Headhunter-Core/internal/store"
)

// profilePace is a small delay between claims so the pool stays polite to the
// external sources (Wikidata, SEC) even at higher concurrency.
const profilePace = 300 * time.Millisecond

// profileSettings reads runtime knobs from config: profile_enabled (default
// true) and company_profile_concurrency (default 2, capped at 10).
func (s *Server) profileSettings(ctx context.Context) (enabled bool, workers int) {
	enabled, workers = true, 2
	if cfg, err := s.Store.GetAllConfig(ctx); err == nil {
		if raw, ok := cfg["profile_enabled"]; ok {
			var b bool
			if json.Unmarshal(raw, &b) == nil {
				enabled = b
			}
		}
		if raw, ok := cfg["company_profile_concurrency"]; ok {
			var v int
			if json.Unmarshal(raw, &v) == nil && v > 0 {
				workers = v
			}
		}
	}
	if workers > 10 {
		workers = 10
	}
	if workers < 1 {
		workers = 1
	}
	return
}

// RunProfiler is the persistent company-profile consumer: it drains never-profiled
// companies, assembling each one's profile from the free deterministic sources.
// DB-claimed, so it resumes after a restart (stale claims reclaimed on boot).
// No LLM here — sourcing is token-free; synthesis is a separate, optional pass.
func (s *Server) RunProfiler(ctx context.Context) {
	if s.Store == nil {
		log.Printf("profiler: disabled (no DB)")
		return
	}
	if n, err := s.Store.ReclaimStaleCompanyClaims(ctx, 0); err == nil && n > 0 {
		log.Printf("profiler: reclaimed %d stale claim(s) on boot", n)
	}
	log.Printf("profiler: consumer started")
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		enabled, workers := s.profileSettings(ctx)
		if !enabled {
			time.Sleep(5 * time.Second)
			continue
		}
		if int(atomic.LoadInt64(&s.profInFlight)) >= workers {
			time.Sleep(750 * time.Millisecond)
			continue
		}
		c, err := s.Store.ClaimNextUnprofiledCompany(ctx, 0) // 0 = only never-profiled; re-profile is manual
		if err != nil {
			log.Printf("profiler: claim: %v", err)
			time.Sleep(5 * time.Second)
			continue
		}
		if c == nil {
			time.Sleep(8 * time.Second) // queue empty; companies are bounded, poll slowly
			continue
		}
		atomic.AddInt64(&s.profInFlight, 1)
		go func(c store.Company) {
			defer atomic.AddInt64(&s.profInFlight, -1)
			docs, _ := s.Store.SampleCompanyDocs(ctx, c.ID, 5)
			h := buildHints(&c, docs)
			prof, flags := enrich.Assemble(ctx, h, enrich.Free())
			body, _ := json.Marshal(prof)
			if err := s.Store.SaveCompanyProfile(ctx, c.ID, body, h.Domain, flags); err != nil {
				log.Printf("profiler: company %d %s: %v", c.ID, c.Name, err)
				_ = s.Store.ReleaseCompanyClaim(ctx, c.ID)
				atomic.AddInt64(&s.profFailed, 1)
				return
			}
			atomic.AddInt64(&s.profDone, 1)
		}(*c)
		time.Sleep(profilePace)
	}
}

// buildHints derives enrichment hints from a company's sample posting docs —
// notably Built In's hiringOrganization.sameAs (the company page URL).
func buildHints(c *store.Company, docs []json.RawMessage) *enrich.Hints {
	h := &enrich.Hints{Name: c.Name, Norm: c.Norm, Docs: docs}
	for _, d := range docs {
		var rp struct {
			Raw struct {
				HiringOrganization struct {
					SameAs string `json:"sameAs"`
				} `json:"hiringOrganization"`
			} `json:"raw"`
		}
		if json.Unmarshal(d, &rp) == nil && rp.Raw.HiringOrganization.SameAs != "" {
			h.BuiltInURL = rp.Raw.HiringOrganization.SameAs
			break
		}
	}
	return h
}

// ---- HTTP handlers ----

func (s *Server) companiesList(w http.ResponseWriter, r *http.Request) {
	if s.Store == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "no database"})
		return
	}
	rows, err := s.Store.ListCompanies(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "companies": rows})
}

func (s *Server) companyGet(w http.ResponseWriter, r *http.Request) {
	if s.Store == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "no database"})
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad id"})
		return
	}
	c, err := s.Store.GetCompany(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if c == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "company": c})
}

func (s *Server) profileStatus(w http.ResponseWriter, r *http.Request) {
	if s.Store == nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "enabled": false})
		return
	}
	enabled, workers := s.profileSettings(r.Context())
	waiting, inFlight, _ := s.Store.CompanyQueueStats(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "enabled": enabled, "workers": workers,
		"waiting": waiting, "inFlight": int(atomic.LoadInt64(&s.profInFlight)), "claimed": inFlight,
		"doneSession": atomic.LoadInt64(&s.profDone), "failedSession": atomic.LoadInt64(&s.profFailed),
	})
}

func (s *Server) profilePause(w http.ResponseWriter, r *http.Request) {
	if s.Store == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "no database"})
		return
	}
	on := r.URL.Query().Get("on") != "false"
	b, _ := json.Marshal(on)
	if err := s.Store.SetConfig(r.Context(), "profile_enabled", b); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "enabled": on})
}

func (s *Server) companiesReprofile(w http.ResponseWriter, r *http.Request) {
	if s.Store == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "no database"})
		return
	}
	n, err := s.Store.ReprofileAll(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "requeued": n})
}
