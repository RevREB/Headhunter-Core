package api

import "net/http"

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
