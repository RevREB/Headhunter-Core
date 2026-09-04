// Package api serves the MCP-shaped HTTP surface for Headhunter-Core.
package api

import (
	"encoding/json"
	"net/http"

	"github.com/RevREB/Headhunter-Core/internal/analytics"
	"github.com/RevREB/Headhunter-Core/internal/engine"
	"github.com/RevREB/Headhunter-Core/internal/store"
	"github.com/RevREB/Headhunter-Core/pkg/scraper"
)

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
	mux.HandleFunc("POST /api/scan/ingest", s.ingest)
	mux.HandleFunc("POST /api/cycle", s.cycle)
	return mux
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) healthz(w http.ResponseWriter, _ *http.Request) {
	_, _ = w.Write([]byte("ok\n"))
}

// tools returns the MCP-shaped manifest the Go MCP proxy mirrors.
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

// ingest accepts scraped RawPostings, dedups+scores them via the engine, and
// persists via the store.
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
			HasApplyURL:      p.URL != "",
			HasSalary:        p.Comp != "",
			HasCompany:       p.Company != "",
			HasLocation:      p.Location != "",
			DescriptionLen:   len(p.Raw),
			PostedWithinDays: -1,
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
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "created": created, "deduped": deduped, "failed": failed,
	})
}

func (s *Server) cycle(w http.ResponseWriter, _ *http.Request) {
	// operator-lite not yet wired; accept and report.
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "accepted", "note": "operator not yet wired"})
}
