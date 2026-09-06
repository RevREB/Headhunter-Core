package api

import (
	"encoding/json"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
)

// cultureStop drops structural words and the ubiquitous eval boilerplate so the
// tally surfaces the actual culture signals.
var cultureStop = map[string]bool{
	"the": true, "and": true, "for": true, "with": true, "not": true, "but": true, "that": true,
	"this": true, "are": true, "has": true, "was": true, "would": true, "their": true, "its": true,
	"vs": true, "vs.": true, "from": true, "any": true, "all": true, "may": true, "one": true,
	"culture": true, "screen": true, "candidate": true, "candidate's": true, "role": true, "roles": true,
	"caution": true, "fail": true, "pass": true, "signals": true, "signal": true, "job": true, "jd": true,
	"which": true, "who": true, "than": true, "into": true, "out": true, "over": true, "some": true,
}

var wordRe = regexp.MustCompile(`[a-z][a-z'-]+`)

func cultureTokens(s string) []string {
	var out []string
	for _, w := range wordRe.FindAllString(strings.ToLower(s), -1) {
		if len(w) >= 3 && !cultureStop[w] {
			out = append(out, w)
		}
	}
	return out
}

func topBuckets(m map[string]int, n int) []map[string]any {
	type kv struct {
		k string
		v int
	}
	var s []kv
	for k, v := range m {
		if v >= 3 { // ignore one-offs
			s = append(s, kv{k, v})
		}
	}
	sort.Slice(s, func(i, j int) bool {
		if s[i].v != s[j].v {
			return s[i].v > s[j].v
		}
		return s[i].k < s[j].k
	})
	out := []map[string]any{}
	for i := 0; i < len(s) && i < n; i++ {
		out = append(out, map[string]any{"key": s[i].k, "count": s[i].v})
	}
	return out
}

// cultureAnalysis empirically summarizes what the culture screen has keyed on:
// verdict counts, top single terms and two-word phrases across the cited
// evidence, and a sample of raw notes.
func (s *Server) cultureAnalysis(w http.ResponseWriter, r *http.Request) {
	if s.Store == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "no database"})
		return
	}
	notes, err := s.Store.CultureNotes(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	uni, bi := map[string]int{}, map[string]int{}
	fails, cautions, withEvidence := 0, 0, 0
	samples := []map[string]string{}
	for _, n := range notes {
		if n.Verdict == "fail" {
			fails++
		} else {
			cautions++
		}
		if n.Evidence == "" {
			continue
		}
		withEvidence++
		toks := cultureTokens(n.Evidence)
		for i, t := range toks {
			uni[t]++
			if i+1 < len(toks) {
				bi[t+" "+toks[i+1]]++
			}
		}
		if len(samples) < 50 {
			samples = append(samples, map[string]string{"verdict": n.Verdict, "company": n.Company, "evidence": n.Evidence})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true,
		"counts": map[string]int{"total": len(notes), "fail": fails, "caution": cautions, "withEvidence": withEvidence},
		"topTerms":   topBuckets(uni, 40),
		"topPhrases": topBuckets(bi, 30),
		"samples":    samples,
	})
}


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
