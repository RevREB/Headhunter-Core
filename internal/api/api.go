// Package api serves the MCP-shaped HTTP surface and the embedded web dashboard.
package api

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"

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
type Server struct {
	Store     *store.Store
	Analytics *analytics.Analytics
	LLM       *llm.Client
}

// New builds the API server.
func New(st *store.Store, an *analytics.Analytics, l *llm.Client) *Server {
	return &Server{Store: st, Analytics: an, LLM: l}
}

// Routes returns the HTTP handler.
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.HandleFunc("GET /api/tools", s.tools)
	mux.HandleFunc("POST /api/tools/{name}", s.callTool)
	mux.HandleFunc("GET /api/applications", s.listApplications)
	mux.HandleFunc("POST /api/applications/{id}/status", s.setStatus)
	mux.HandleFunc("POST /api/scan/ingest", s.ingest)
	mux.HandleFunc("POST /api/import/tracker", s.importTracker)
	mux.HandleFunc("GET /api/config", s.getConfig)
	mux.HandleFunc("POST /api/config/{key}", s.setConfig)
	mux.HandleFunc("POST /api/cycle", s.cycle)
	mux.HandleFunc("POST /api/ask", s.ask)
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

func (s *Server) cycle(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "accepted", "note": "operator not yet wired"})
}
