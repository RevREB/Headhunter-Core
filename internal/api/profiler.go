package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/RevREB/Headhunter-Core/internal/enrich"
	"github.com/RevREB/Headhunter-Core/internal/llm"
	"github.com/RevREB/Headhunter-Core/internal/store"
)

// hardExclusionFlags are company flags that hard-stop every posting from that
// company deterministically (no LLM call). Derived by the enricher.
var hardExclusionFlags = map[string]bool{"casino": true, "gambling": true, "ruled_out": true}

// hardExclusion returns the first hard-exclusion flag present, or "".
func hardExclusion(flags []string) string {
	for _, f := range flags {
		if hardExclusionFlags[f] {
			return f
		}
	}
	return ""
}

// excludedReport is the stub A–G-shaped report stored for a company hard-stopped
// by a flag, so the record is consistent with LLM-produced evaluations.
func excludedReport(name, flag string) json.RawMessage {
	md := fmt.Sprintf("# Evaluation: %s\n\n**Hard stop — company excluded (%s).**\n\n"+
		"This company matched a standing exclusion, so it was scored 0.3 without a full "+
		"evaluation (no tokens spent). Clear the company's flag on its profile to re-evaluate.\n", name, flag)
	summary, _ := json.Marshal(map[string]any{
		"score": 0.3, "decision": "hard_stop", "hard_stops": []string{"company excluded: " + flag},
	})
	doc, _ := json.Marshal(map[string]any{"markdown": md, "summary": json.RawMessage(summary)})
	return doc
}

// compactCompanyContext renders a company's sourced fields (+ flags, summary) as
// a compact JSON block for the evaluator, or "" when nothing is known.
func compactCompanyContext(cc *store.CompanyContext) string {
	var p struct {
		Fields  map[string]json.RawMessage `json:"fields"`
		Summary string                     `json:"summary"`
	}
	_ = json.Unmarshal(cc.Profile, &p)
	if len(p.Fields) == 0 && p.Summary == "" {
		return ""
	}
	out, _ := json.Marshal(map[string]any{
		"name": cc.Name, "flags": cc.Flags, "fields": p.Fields, "summary": p.Summary,
	})
	return string(out)
}

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
	// Lazy, cheap synthesis: only for companies you actually view, once.
	s.maybeSynthesize(c.ID, c.Name, c.Profile)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "company": c})
}

// maybeSynthesize composes a short, sourced-facts-only company briefing on a
// cheap model (SYNTH_MODEL), off the request path, once per company. It's the
// only LLM cost in the whole subsystem, and it's cached in the profile.
func (s *Server) maybeSynthesize(id int64, name string, profile json.RawMessage) {
	if s.LLM == nil {
		return
	}
	var p struct {
		Fields  map[string]json.RawMessage `json:"fields"`
		Summary string                     `json:"summary"`
	}
	_ = json.Unmarshal(profile, &p)
	if p.Summary != "" || len(p.Fields) == 0 {
		return
	}
	if _, busy := s.synthesizing.LoadOrStore(id, true); busy {
		return
	}
	go func() {
		defer s.synthesizing.Delete(id)
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		fieldsJSON, _ := json.Marshal(p.Fields)
		sys := "You write a two-sentence, neutral company briefing for a job seeker, using ONLY the provided sourced facts. Never invent facts; if the facts are thin, keep it short and say so. No preamble, no markdown headers."
		usr := fmt.Sprintf("Company: %s\nSourced facts (JSON):\n%s\n\nWrite the briefing (<=2 sentences).", name, fieldsJSON)
		out, err := s.LLM.CompleteModel(ctx, os.Getenv("SYNTH_MODEL"),
			[]llm.Msg{{Role: "system", Content: sys}, {Role: "user", Content: usr}}, 0.3, 220)
		if err != nil {
			log.Printf("synth company %d: %v", id, err)
			return
		}
		if out = strings.TrimSpace(out); out != "" {
			_ = s.Store.SetCompanySummary(context.Background(), id, out)
		}
	}()
}

func (s *Server) companySimilar(w http.ResponseWriter, r *http.Request) {
	if s.Store == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "no database"})
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad id"})
		return
	}
	rows, err := s.Store.SimilarCompanies(r.Context(), id, 12)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "companies": rows})
}

func (s *Server) discoveries(w http.ResponseWriter, r *http.Request) {
	if s.Store == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "no database"})
		return
	}
	d, err := s.Store.ATSDiscoveries(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "discoveries": d})
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
