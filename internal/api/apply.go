package api

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/RevREB/Headhunter-Core/internal/webmcp"
)

// webmcpURL is the in-cluster Playwright-MCP endpoint Core drives.
func webmcpURL() string {
	if v := os.Getenv("WEBMCP_MCP_URL"); v != "" {
		return v
	}
	return "http://headhunter-webmcp.headhunter.svc.cluster.local:8931/mcp"
}

// applyProfile is the identity/contact subset of the candidate profile that is
// SAFE to auto-fill. Everything else (EEO, work-auth attestations, open-ended
// questions, consent) is deliberately left for the human.
type applyProfile struct {
	Name     string `json:"name"`
	Email    string `json:"email"`
	Phone    string `json:"phone"`
	GitHub   string `json:"github"`
	LinkedIn string `json:"linkedin"`
	Address  string `json:"address"`
	Domicile string `json:"domicile"`
	Pronouns string `json:"pronouns"`
}

// snapField is a form field parsed from the accessibility snapshot.
type snapField struct {
	Role string
	Name string
	Ref  string
}

// snapRe matches a snapshot line: - textbox "Email address" [ref=e11]
var snapRe = regexp.MustCompile(`(?m)-\s+(textbox|combobox|checkbox|radio|slider)\s+"([^"]*)"\s+\[ref=(e\d+)\]`)

func parseSnapshot(snap string) []snapField {
	var out []snapField
	for _, m := range snapRe.FindAllStringSubmatch(snap, -1) {
		out = append(out, snapField{Role: m[1], Name: m[2], Ref: m[3]})
	}
	return out
}

// neverFill guards fields we must not auto-answer: attestations, EEO/demographic,
// work-authorization declarations, consent, and open-ended questions.
var neverFill = regexp.MustCompile(`(?i)sponsor|visa|authoriz|work permit|eligible to work|legally|veteran|disab|gender|\brace\b|ethnic|hispanic|\bage\b|salary|compensation|expected pay|desired|how did you hear|referr|reference|citizen|clearance|felon|crimin|consent|agree|certif|confirm|acknowledg|cover letter|why |describe|do you |have you |are you `)

// matchValue maps a field's accessible name to a safe profile value, or ("", false).
func matchValue(name string, p applyProfile) (string, bool) {
	n := strings.ToLower(strings.TrimSpace(name))
	if n == "" || neverFill.MatchString(n) {
		return "", false
	}
	first, last := splitName(p.Name)
	street, city, state, zip := splitAddress(p.Address)
	switch {
	case has(n, "first name", "given name", "first"):
		return first, first != ""
	case has(n, "last name", "surname", "family name"):
		return last, last != ""
	case has(n, "full name", "legal name", "your name") || n == "name":
		return p.Name, p.Name != ""
	case has(n, "e-mail", "email"):
		return p.Email, p.Email != ""
	case has(n, "phone", "telephone", "mobile", "cell"):
		return p.Phone, p.Phone != ""
	case has(n, "linkedin"):
		return p.LinkedIn, p.LinkedIn != ""
	case has(n, "github"):
		return p.GitHub, p.GitHub != ""
	case has(n, "portfolio", "website", "personal site", "url"):
		return p.GitHub, p.GitHub != ""
	case has(n, "street", "address line") || n == "address":
		return street, street != ""
	case has(n, "city", "town"):
		return city, city != ""
	case has(n, "zip", "postal"):
		return zip, zip != ""
	case (has(n, "state", "province", "region")) && !strings.Contains(n, "united"):
		return state, state != ""
	case has(n, "pronoun"):
		return p.Pronouns, p.Pronouns != ""
	case has(n, "location", "based", "where are you"):
		return p.Domicile, p.Domicile != ""
	}
	return "", false
}

func has(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

func splitName(name string) (first, last string) {
	f := strings.Fields(name)
	if len(f) > 0 {
		first = f[0]
	}
	if len(f) > 1 {
		last = f[len(f)-1]
	}
	return
}

func splitAddress(addr string) (street, city, state, zip string) {
	parts := strings.Split(addr, ",")
	if len(parts) > 0 {
		street = strings.TrimSpace(parts[0])
	}
	if len(parts) > 1 {
		city = strings.TrimSpace(parts[1])
	}
	if len(parts) > 2 {
		sz := strings.Fields(strings.TrimSpace(parts[2]))
		if len(sz) > 0 {
			state = sz[0]
		}
		if len(sz) > 1 {
			zip = sz[len(sz)-1]
		}
	}
	return
}

// applyResult is the handoff report returned to the UI.
type applyResult struct {
	URL       string   `json:"url"`
	Filled    []string `json:"filled"`
	Remaining []string `json:"remaining"` // unmatched text fields the human should complete
	Fields    int      `json:"fields"`
	Handoff   string   `json:"handoff"`
}

// prepareApplication drives WebMCP to open the URL and fill the safe identity
// fields, then STOPS (never submits) so the human can finish and submit via noVNC.
func (s *Server) prepareApplication(ctx context.Context, applyURL string) (*applyResult, error) {
	var p applyProfile
	if cfg, err := s.Store.GetAllConfig(ctx); err == nil {
		if raw, ok := cfg["profile"]; ok {
			_ = json.Unmarshal(raw, &p)
		}
	}
	cl := webmcp.New(webmcpURL())
	if err := cl.Initialize(ctx); err != nil {
		return nil, err
	}
	if err := cl.Navigate(ctx, applyURL); err != nil {
		return nil, err
	}
	snap, err := cl.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	res := &applyResult{URL: applyURL, Filled: []string{}, Remaining: []string{}}
	var fills []webmcp.FillField
	for _, f := range parseSnapshot(snap) {
		if f.Role != "textbox" && f.Role != "combobox" {
			continue // never auto-touch checkboxes/radios/sliders (consent, EEO, options)
		}
		res.Fields++
		if v, ok := matchValue(f.Name, p); ok {
			fills = append(fills, webmcp.FillField{Name: f.Name, Type: f.Role, Ref: f.Ref, Value: v})
			res.Filled = append(res.Filled, f.Name)
		} else {
			res.Remaining = append(res.Remaining, f.Name)
		}
	}
	if err := cl.FillForm(ctx, fills); err != nil {
		return nil, err
	}
	res.Handoff = "Filled the safe identity fields. Take over to review and submit: " +
		"kubectl -n headhunter port-forward svc/headhunter-webmcp 6080:6080 then open http://localhost:6080/vnc.html"
	return res, nil
}

func (s *Server) applyJob(w http.ResponseWriter, r *http.Request) {
	if s.Store == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "no database"})
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad id"})
		return
	}
	app, err := s.Store.GetApplication(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if app == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	target := app.URL
	if q := r.URL.Query().Get("url"); q != "" {
		target = q // allow overriding with the actual application-form URL
	}
	if target == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "no URL to open for this application"})
		return
	}
	res, err := s.prepareApplication(r.Context(), target)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "webmcp: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "apply": res})
}
