// Package api serves the MCP-shaped HTTP surface and the embedded web dashboard.
package api

import (
	"embed"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strconv"

	"github.com/RevREB/Headhunter-Core/internal/analytics"
	"github.com/RevREB/Headhunter-Core/internal/engine"
	"github.com/RevREB/Headhunter-Core/internal/importer"
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
}

// New builds the API server.
func New(st *store.Store, an *analytics.Analytics) *Server {
	return &Server{Store: st, Analytics: an}
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
