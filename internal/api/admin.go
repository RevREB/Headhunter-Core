package api

import (
	"encoding/json"
	"net/http"
	"os"
	"regexp"
	"strings"
)

// urlLineRe pulls the JD URL from a career-ops report: the explicit "URL:" line
// first (most reliable), then the machine-summary `url:` field.
var (
	urlLineRe = regexp.MustCompile(`(?i)URL:\**\s*(https?://[^\s)\]]+)`)
	urlYamlRe = regexp.MustCompile(`(?im)^\s*url:\s*"?(https?://[^\s")\]]+)`)
	anyURLRe  = regexp.MustCompile(`https?://[^\s)\]]+`)
)

// extractReportURL finds the job's application URL inside a report's markdown.
// It prefers the labeled URL line, then the machine-summary url, then the first
// non-Headhunter/non-builtin http URL. Trailing punctuation is trimmed.
func extractReportURL(md string) string {
	clean := func(u string) string { return strings.TrimRight(u, ".,);\"'") }
	if m := urlLineRe.FindStringSubmatch(md); m != nil {
		return clean(m[1])
	}
	if m := urlYamlRe.FindStringSubmatch(md); m != nil {
		return clean(m[1])
	}
	for _, u := range anyURLRe.FindAllString(md, -1) {
		u = clean(u)
		if strings.Contains(u, "builtin.com") || strings.Contains(u, "127.0.0.1") || strings.Contains(u, "localhost") {
			continue
		}
		return u
	}
	return ""
}

// remediate runs the data fix. Dry-run by default; ?apply=true commits. Guarded
// by IMPORT_TOKEN like the import endpoints, since it mutates many records.
func (s *Server) remediate(w http.ResponseWriter, r *http.Request) {
	token := os.Getenv("IMPORT_TOKEN")
	if token == "" || r.Header.Get("X-Import-Token") != token {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
		return
	}
	if s.Store == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "no database"})
		return
	}
	apply := r.URL.Query().Get("apply") == "true"
	cand, upd, err := s.Store.BackfillURLsFromReports(r.Context(), apply, extractReportURL)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "backfill: " + err.Error()})
		return
	}
	rq, err := s.Store.RequeueUnreported(r.Context(), apply)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "requeue: " + err.Error()})
		return
	}
	out := map[string]any{"ok": true, "apply": apply, "urlsRecoverable": cand, "urlsBackfilled": upd, "requeued": rq}
	if apply {
		if rep, err := s.Store.AuditIntegrity(r.Context()); err == nil {
			out["auditAfter"] = rep
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// applyMinScore reads the config'd minimum apply score (the below/above line),
// defaulting to 3.0.
func (s *Server) applyMinScore(r *http.Request) float64 {
	min := 3.0
	if cfg, err := s.Store.GetAllConfig(r.Context()); err == nil {
		if raw, ok := cfg["apply_min_score"]; ok {
			var v float64
			if json.Unmarshal(raw, &v) == nil && v > 0 {
				min = v
			}
		}
	}
	return min
}

// discardBelowLine bulk-moves evaluated records scoring below apply_min_score to
// discarded (reason below_bar). A human-invoked UI action (the "Clean below the
// line" button), consistent with the unguarded per-record discard; dry-run
// unless ?apply=true so the button can show the count first.
func (s *Server) discardBelowLine(w http.ResponseWriter, r *http.Request) {
	if s.Store == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "no database"})
		return
	}
	min := s.applyMinScore(r)
	apply := r.URL.Query().Get("apply") == "true"
	n, err := s.Store.DiscardBelowLine(r.Context(), min, apply)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "apply": apply, "minScore": min, "discarded": n})
}

// tuning splits the discard pile into below-the-line (scan-tuning) and pass-overs
// (rubric-tuning), against the config'd apply_min_score.
func (s *Server) tuning(w http.ResponseWriter, r *http.Request) {
	if s.Store == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "no database"})
		return
	}
	rep, err := s.Store.DiscardTuning(r.Context(), s.applyMinScore(r))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "tuning": rep})
}

// auditIntegrity is a read-only data-integrity report — the forensic basis for a
// remediation and the way to verify one afterward.
func (s *Server) auditIntegrity(w http.ResponseWriter, r *http.Request) {
	if s.Store == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "no database"})
		return
	}
	rep, err := s.Store.AuditIntegrity(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "audit": rep})
}
