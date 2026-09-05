// Package enrich assembles a provenance-tagged company profile from free,
// structured sources. Sourcing is deterministic (HTTP + parse, no LLM); the only
// token cost — a synthesis pass — lives elsewhere and is optional for scoring.
// Enrichers are pluggable: a new source (e.g. a licensed Crunchbase API) is a
// single Enricher, no rearchitecting.
package enrich

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// Field is one provenance-tagged company fact. Source "inferred" marks an LLM
// guess (never gate-eligible downstream); any other source is deterministic.
type Field struct {
	Key    string `json:"-"`
	Value  any    `json:"value"`
	Source string `json:"source"`
	Detail string `json:"detail,omitempty"`
}

// Hints is what the profiler knows about a company before enrichment. Domain is
// discovered mid-run (by BuiltIn) and reused by later enrichers.
type Hints struct {
	Name       string
	Norm       string
	Domain     string
	BuiltInURL string
	ApplyHosts []string
	Docs       []json.RawMessage
}

// Enricher pulls provenance-tagged fields for a company from one source.
type Enricher interface {
	Name() string
	Enrich(ctx context.Context, h *Hints) ([]Field, error)
}

// Profile is the assembled, provenance-tagged company profile.
type Profile struct {
	Fields       map[string]Field `json:"fields"`
	Summary      string           `json:"summary,omitempty"`
	SourcesTried []string         `json:"sourcesTried"`
}

// authority ranks sources; on a key collision the higher rank wins.
func authority(source string) int {
	switch source {
	case "sec_edgar":
		return 4
	case "wikidata":
		return 3
	case "builtin", "hh_derived":
		return 2
	case "inferred":
		return 0
	default:
		return 1
	}
}

// Free returns the deterministic, no-key enricher stack.
func Free() []Enricher {
	return []Enricher{BuiltIn{}, Wikidata{}, EDGAR{}, ATS{}}
}

// Assemble runs the enrichers in order, merging fields by authority, and derives
// deterministic flags (e.g. casino). Enricher errors are logged and skipped.
func Assemble(ctx context.Context, h *Hints, enrichers []Enricher) (Profile, []string) {
	prof := Profile{Fields: map[string]Field{}}
	for _, e := range enrichers {
		prof.SourcesTried = append(prof.SourcesTried, e.Name())
		fields, err := e.Enrich(ctx, h)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[enrich] %s (%s): %v\n", h.Name, e.Name(), err)
			continue
		}
		for _, f := range fields {
			if cur, ok := prof.Fields[f.Key]; !ok || authority(f.Source) > authority(cur.Source) {
				prof.Fields[f.Key] = f
			}
			// Let BuiltIn's website discovery feed later enrichers.
			if f.Key == "website" && h.Domain == "" {
				if s, ok := f.Value.(string); ok {
					h.Domain = hostOf(s)
				}
			}
		}
	}
	return prof, deriveFlags(prof)
}

// deriveFlags maps profile facts to deterministic exclusion flags. Extend as new
// exclusions arise; this is what the evaluator's hard-stop gate reads.
func deriveFlags(p Profile) []string {
	var flags []string
	hay := strings.ToLower(fieldString(p, "industry") + " " + fieldString(p, "description"))
	for _, kw := range []string{"casino", "gambling", "sportsbook", "igaming"} {
		if strings.Contains(hay, kw) {
			flags = append(flags, "casino")
			break
		}
	}
	return flags
}

func fieldString(p Profile, key string) string {
	f, ok := p.Fields[key]
	if !ok {
		return ""
	}
	switch v := f.Value.(type) {
	case string:
		return v
	case []any:
		var parts []string
		for _, e := range v {
			parts = append(parts, fmt.Sprint(e))
		}
		return strings.Join(parts, " ")
	default:
		return fmt.Sprint(v)
	}
}

// ---- shared HTTP ----

var client = &http.Client{Timeout: 20 * time.Second}

// userAgent identifies the crawler. SEC EDGAR requires a UA that carries a
// contact email (a URL-only or browser UA is 403'd), so the deployment should
// set ENRICH_UA to "<name> <email>" to activate the EDGAR enricher; without it,
// EDGAR simply no-ops while the other sources work.
func userAgent() string {
	if v := os.Getenv("ENRICH_UA"); v != "" {
		return v
	}
	return "Headhunter-Core/1.0 (+https://github.com/RevREB/Headhunter-Core)"
}

func httpGet(ctx context.Context, u string, accept string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent())
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 8<<20))
}

// hostOf returns the bare host (no www.) of a URL, or "" if unparseable.
func hostOf(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return ""
	}
	return strings.TrimPrefix(strings.ToLower(u.Host), "www.")
}
