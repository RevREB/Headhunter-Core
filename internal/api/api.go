// Package api serves the MCP-shaped HTTP surface and the embedded web dashboard.
package api

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/RevREB/Headhunter-Core/internal/analytics"
	"github.com/RevREB/Headhunter-Core/internal/engine"
	"github.com/RevREB/Headhunter-Core/internal/importer"
	"github.com/RevREB/Headhunter-Core/internal/llm"
	"github.com/RevREB/Headhunter-Core/internal/store"
	"github.com/RevREB/Headhunter-Core/pkg/scraper"
)

//go:embed web
var webFS embed.FS

// Server holds the API dependencies. Store/Analytics may be nil (health-only
// degraded mode when DATABASE_URL is unset).
// Cycler launches a scan cycle (the operator). An interface so this package
// stays decoupled from client-go.
type Cycler interface {
	RunCycle(ctx context.Context) (int, error)
}

type Server struct {
	Store     *store.Store
	Analytics *analytics.Analytics
	LLM       *llm.Client
	Operator  Cycler
}

// New builds the API server.
func New(st *store.Store, an *analytics.Analytics, l *llm.Client, op Cycler) *Server {
	return &Server{Store: st, Analytics: an, LLM: l, Operator: op}
}

// Routes returns the HTTP handler.
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.HandleFunc("GET /api/tools", s.tools)
	mux.HandleFunc("POST /api/tools/{name}", s.callTool)
	mux.HandleFunc("GET /api/applications", s.listApplications)
	mux.HandleFunc("GET /api/applications/{id}", s.getApplication)
	mux.HandleFunc("POST /api/applications/{id}/status", s.setStatus)
	mux.HandleFunc("POST /api/scan/ingest", s.ingest)
	mux.HandleFunc("POST /api/import/tracker", s.importTracker)
	mux.HandleFunc("GET /api/config", s.getConfig)
	mux.HandleFunc("POST /api/config/{key}", s.setConfig)
	mux.HandleFunc("POST /api/cycle", s.cycle)
	mux.HandleFunc("POST /api/evaluate", s.evaluate)
	mux.HandleFunc("POST /api/ask", s.ask)
	mux.HandleFunc("GET /icon.png", s.icon)
	mux.HandleFunc("GET /favicon.ico", s.icon)
	mux.HandleFunc("GET /", s.index)
	return mux
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	b, err := webFS.ReadFile("web/index.html")
	if err != nil {
		http.Error(w, "ui unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store") // always serve fresh UI after a deploy
	_, _ = w.Write(b)
}

func (s *Server) icon(w http.ResponseWriter, _ *http.Request) {
	b, err := webFS.ReadFile("web/icon.png")
	if err != nil {
		http.NotFound(w, nil)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	_, _ = w.Write(b)
}

func (s *Server) healthz(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok\n")) }

func (s *Server) tools(w http.ResponseWriter, _ *http.Request) {
	type tool struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		InputSchema any    `json:"inputSchema"`
	}
	empty := map[string]any{"type": "object", "properties": map[string]any{}}
	writeJSON(w, http.StatusOK, map[string]any{"tools": []tool{
		{Name: "stats", Description: "Application funnel counts by status", InputSchema: empty},
		{Name: "cycle", Description: "Trigger a scan/evaluate cycle", InputSchema: empty},
	}})
}

func (s *Server) callTool(w http.ResponseWriter, r *http.Request) {
	switch r.PathValue("name") {
	case "stats":
		if s.Analytics == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "no database"})
			return
		}
		f, err := s.Analytics.Funnel(r.Context())
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": f})
	case "cycle":
		s.cycle(w, r)
	default:
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown tool"})
	}
}

func (s *Server) listApplications(w http.ResponseWriter, r *http.Request) {
	if s.Store == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "no database"})
		return
	}
	limit := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	apps, err := s.Store.ListApplications(r.Context(), limit, r.URL.Query().Get("status"))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "applications": apps})
}

func (s *Server) getApplication(w http.ResponseWriter, r *http.Request) {
	if s.Store == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "no database"})
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad id"})
		return
	}
	d, err := s.Store.GetApplication(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "application": d})
}

func (s *Server) setStatus(w http.ResponseWriter, r *http.Request) {
	if s.Store == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "no database"})
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad id"})
		return
	}
	var body struct {
		To string `json:"to"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "expected {\"to\":\"...\"}"})
		return
	}
	if err := s.Store.SetStatus(r.Context(), id, body.To); err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": id, "status": body.To})
}

// importTracker ingests an applications.md document (full reload). Guarded by
// IMPORT_TOKEN: disabled unless the env is set and the request header matches.
func (s *Server) importTracker(w http.ResponseWriter, r *http.Request) {
	token := os.Getenv("IMPORT_TOKEN")
	if token == "" {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "import disabled (IMPORT_TOKEN unset)"})
		return
	}
	if r.Header.Get("X-Import-Token") != token {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
		return
	}
	if s.Store == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "no database"})
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 16<<20))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "read body: " + err.Error()})
		return
	}
	rows := importer.ParseTracker(string(body))
	n, err := s.Store.LoadTracker(r.Context(), rows)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error(), "loaded": strconv.Itoa(n)})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "parsed": len(rows), "imported": n})
}

func (s *Server) getConfig(w http.ResponseWriter, r *http.Request) {
	if s.Store == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "no database"})
		return
	}
	cfg, err := s.Store.GetAllConfig(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "config": cfg})
}

// setConfig stores a config document under {key}. A JSON body is stored as-is;
// any other body (e.g. raw markdown for cv) is stored as a JSON string.
func (s *Server) setConfig(w http.ResponseWriter, r *http.Request) {
	if s.Store == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "no database"})
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 8<<20))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	doc := json.RawMessage(body)
	if !json.Valid(body) {
		enc, _ := json.Marshal(string(body))
		doc = json.RawMessage(enc)
	}
	if err := s.Store.SetConfig(r.Context(), r.PathValue("key"), doc); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "key": r.PathValue("key")})
}

func (s *Server) ask(w http.ResponseWriter, r *http.Request) {
	if s.LLM == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "assistant not configured"})
		return
	}
	var req struct {
		Messages []llm.Msg `json:"messages"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Messages) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "expected {messages:[...]}"})
		return
	}
	msgs := append([]llm.Msg{{Role: "system", Content: s.systemPrompt(r.Context())}}, req.Messages...)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	flusher, _ := w.(http.Flusher)
	send := func(obj any) {
		b, _ := json.Marshal(obj)
		fmt.Fprintf(w, "data: %s\n\n", b)
		if flusher != nil {
			flusher.Flush()
		}
	}
	if err := s.LLM.Stream(r.Context(), msgs, func(delta string) { send(map[string]string{"content": delta}) }); err != nil {
		send(map[string]string{"error": err.Error()})
	}
	fmt.Fprint(w, "data: [DONE]\n\n")
	if flusher != nil {
		flusher.Flush()
	}
}

func (s *Server) systemPrompt(ctx context.Context) string {
	var b strings.Builder
	b.WriteString("You are the Headhunter assistant, embedded in a self-hosted job-search dashboard for one user. Be concise, practical, and specific.\n")
	if s.Analytics != nil {
		if f, err := s.Analytics.Funnel(ctx); err == nil {
			b.WriteString("Funnel: ")
			for _, k := range []string{"evaluated", "applied", "responded", "interview", "offer", "hired", "rejected", "discarded", "skip"} {
				if f[k] > 0 {
					fmt.Fprintf(&b, "%s=%d ", k, f[k])
				}
			}
			b.WriteString("\n")
		}
	}
	if s.Store != nil {
		if apps, err := s.Store.ListApplications(ctx, 300, "evaluated"); err == nil {
			sort.Slice(apps, func(i, j int) bool {
				si, sj := 0.0, 0.0
				if apps[i].Score != nil {
					si = *apps[i].Score
				}
				if apps[j].Score != nil {
					sj = *apps[j].Score
				}
				return si > sj
			})
			b.WriteString("Top postings awaiting the user's decision (highest score first):\n")
			for i, a := range apps {
				if i >= 12 {
					break
				}
				sc := "\u2014"
				if a.Score != nil {
					sc = fmt.Sprintf("%.1f", *a.Score)
				}
				fmt.Fprintf(&b, "- %s \u2014 %s (%s)\n", a.Company, a.Role, sc)
			}
		}
	}
	b.WriteString("\nYou can drive the UI by emitting an action tag alone on its own line:\n")
	b.WriteString("  <<act:navigate {\"view\":\"VIEW\"}>>  VIEW in: today, explore, pipeline, followups, portals, analytics, cv, config\n")
	b.WriteString("  <<act:filter {\"status\":\"STATUS\"}>>  filter the Pipeline (also navigates there)\n")
	b.WriteString("Emit an action ONLY when the user clearly wants to navigate or filter; otherwise just answer.")
	return b.String()
}

func (s *Server) evaluate(w http.ResponseWriter, r *http.Request) {
	if s.LLM == nil || s.Store == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "evaluator not configured (LLM or DB missing)"})
		return
	}
	limit := 10
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	if limit > 300 {
		limit = 300
	}
	apps, err := s.Store.ListApplications(r.Context(), limit, "inbox")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	cv := ""
	if cfg, err := s.Store.GetAllConfig(r.Context()); err == nil {
		if raw, ok := cfg["cv"]; ok {
			_ = json.Unmarshal(raw, &cv)
		}
	}
	if len(cv) > 6000 {
		cv = cv[:6000]
	}
	// Evaluate concurrently — each posting is an independent LLM round-trip, so a
	// small worker pool turns an hours-long sequential sweep into minutes. Tunable
	// via EVAL_CONCURRENCY (Bifrost is the limiter; retries absorb transient 429s).
	workers := 6
	if v := os.Getenv("EVAL_CONCURRENCY"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			workers = n
		}
	}
	if v := r.URL.Query().Get("workers"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			workers = n
		}
	}
	if workers > 20 {
		workers = 20
	}
	// The evaluator only RATES — every posting moves inbox -> evaluated with its
	// score. Discarding (like applying or skipping) is the user's decision, made
	// from the dashboard; the engine never auto-cuts a posting.
	var (
		mu                sync.Mutex
		evaluated, failed int
		wg                sync.WaitGroup
		sem               = make(chan struct{}, workers)
	)
	for _, a := range apps {
		wg.Add(1)
		sem <- struct{}{}
		go func(a store.Application) {
			defer wg.Done()
			defer func() { <-sem }()
			doc, _ := s.Store.GetPostingDoc(r.Context(), a.ID)
			score, report, err := s.evalOne(r.Context(), cv, a, doc)
			if err != nil {
				log.Printf("evaluate: app %d %s / %s: %v", a.ID, a.Company, a.Role, err)
				mu.Lock()
				failed++
				mu.Unlock()
				return
			}
			if err := s.Store.SaveEvaluation(r.Context(), a.ID, score, "evaluated", report); err != nil {
				log.Printf("evaluate: save app %d: %v", a.ID, err)
				mu.Lock()
				failed++
				mu.Unlock()
				return
			}
			mu.Lock()
			evaluated++
			mu.Unlock()
		}(a)
	}
	wg.Wait()
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "processed": len(apps), "evaluated": evaluated, "failed": failed,
	})
}

func (s *Server) evalOne(ctx context.Context, cv string, a store.Application, postingDoc json.RawMessage) (float64, json.RawMessage, error) {
	sys := "You are a precise, calibrated job-fit evaluator for a candidate's job search. " +
		"Given the candidate's resume and one job posting (title, location, compensation, and " +
		"description when available), score the fit from 0.0 to 5.0 " +
		"(>=4 strong, 3-4 plausible, <3 weak). Weigh the actual responsibilities and requirements " +
		"in the description, not just the title. Be honest. " +
		"Return ONLY compact JSON, no prose, no code fences: " +
		`{"score":<float>,"verdict":"<one sentence>","strengths":["..."],"concerns":["..."],"hard_stops":["..."]}`
	usr := fmt.Sprintf("CANDIDATE RESUME:\n%s\n\nJOB POSTING:\n%s\nScore the fit.", cv, jobContext(a, postingDoc))
	msgs := []llm.Msg{{Role: "system", Content: sys}, {Role: "user", Content: usr}}

	// The gateway occasionally returns an empty/truncated body; retry a few times.
	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		if attempt > 1 {
			time.Sleep(400 * time.Millisecond)
		}
		resp, err := s.LLM.Complete(ctx, msgs)
		if err != nil {
			lastErr = err
			continue
		}
		var ev struct {
			Score float64 `json:"score"`
		}
		j := extractJSON(resp)
		if err := json.Unmarshal(j, &ev); err != nil {
			lastErr = fmt.Errorf("parse eval (attempt %d, resp %dB): %w", attempt, len(resp), err)
			continue
		}
		if ev.Score < 0 {
			ev.Score = 0
		}
		if ev.Score > 5 {
			ev.Score = 5
		}
		return ev.Score, json.RawMessage(j), nil
	}
	return 0, nil, lastErr
}

// jobContext builds the posting block for the evaluator: title + company always,
// plus location, compensation, and a cleaned description when the scraped posting
// carries them.
func jobContext(a store.Application, doc json.RawMessage) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Company: %s\nRole: %s\n", a.Company, a.Role)
	if len(doc) > 0 {
		var p struct {
			Location string          `json:"location"`
			Comp     string          `json:"comp"`
			PostedAt string          `json:"postedAt"`
			Raw      json.RawMessage `json:"raw"`
		}
		if json.Unmarshal(doc, &p) == nil {
			if p.Location != "" {
				fmt.Fprintf(&b, "Location: %s\n", p.Location)
			}
			if p.Comp != "" {
				fmt.Fprintf(&b, "Compensation: %s\n", p.Comp)
			}
			if p.PostedAt != "" {
				fmt.Fprintf(&b, "Posted: %s\n", p.PostedAt)
			}
			if desc := extractDescription(p.Raw); desc != "" {
				if len(desc) > 4000 {
					desc = desc[:4000]
				}
				fmt.Fprintf(&b, "\nDescription:\n%s\n", desc)
			}
		}
	}
	return b.String()
}

// extractDescription best-effort pulls a job description out of an ATS raw record:
// the longest string field whose key looks like a description/content/body,
// HTML-stripped. Returns "" when the raw record has no such field.
func extractDescription(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return ""
	}
	best := ""
	for k, v := range m {
		s, ok := v.(string)
		if !ok || s == "" {
			continue
		}
		lk := strings.ToLower(k)
		if strings.Contains(lk, "descript") || strings.Contains(lk, "content") || lk == "body" || lk == "text" || lk == "summary" {
			if len(s) > len(best) {
				best = s
			}
		}
	}
	return stripHTML(best)
}

// stripHTML removes tags, unescapes entities, and collapses whitespace.
func stripHTML(s string) string {
	if s == "" {
		return ""
	}
	var b strings.Builder
	inTag := false
	for _, r := range s {
		switch {
		case r == '<':
			inTag = true
		case r == '>':
			inTag = false
			b.WriteByte(' ')
		case !inTag:
			b.WriteRune(r)
		}
	}
	return strings.Join(strings.Fields(html.UnescapeString(b.String())), " ")
}

// extractJSON pulls the first {...} object out of a model reply (tolerating
// code fences or surrounding prose).
func extractJSON(s string) []byte {
	s = strings.TrimSpace(s)
	i := strings.IndexByte(s, '{')
	j := strings.LastIndexByte(s, '}')
	if i >= 0 && j > i {
		return []byte(s[i : j+1])
	}
	return []byte(s)
}

func (s *Server) ingest(w http.ResponseWriter, r *http.Request) {
	if s.Store == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "no database"})
		return
	}
	var postings []scraper.RawPosting
	if err := json.NewDecoder(r.Body).Decode(&postings); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "expected a JSON array of RawPosting"})
		return
	}
	var created, deduped, failed int
	for _, p := range postings {
		normURL := engine.NormalizeURL(p.URL)
		fp := engine.SimHash(p.Title + " " + p.Company + " " + string(p.Raw))
		trust, _ := engine.TrustScore(engine.PostingSignals{
			HasApplyURL: p.URL != "", HasSalary: p.Comp != "", HasCompany: p.Company != "",
			HasLocation: p.Location != "", DescriptionLen: len(p.Raw), PostedWithinDays: -1,
		})
		raw, _ := json.Marshal(p)
		res, err := s.Store.IngestPosting(r.Context(), normURL, "", p.Company, p.Title, fp, trust, raw)
		if err != nil {
			failed++
			continue
		}
		if res.Created {
			created++
		} else {
			deduped++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "created": created, "deduped": deduped, "failed": failed})
}

func (s *Server) cycle(w http.ResponseWriter, r *http.Request) {
	if s.Operator == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "operator not configured"})
		return
	}
	n, err := s.Operator.RunCycle(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"status": "launched", "scrapers": n})
}
