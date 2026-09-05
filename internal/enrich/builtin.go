package enrich

import (
	"context"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
)

// BuiltIn reads the company's Built In page (URL from a posting's
// hiringOrganization.sameAs), which embeds a schema.org Organization JSON-LD
// with founded year, employee count, HQ, industry, website and description.
type BuiltIn struct{}

func (BuiltIn) Name() string { return "builtin" }

// ldRe matches Built In's JSON-LD script whose type has '+' HTML-entity-encoded.
var ldRe = regexp.MustCompile(`(?is)<script[^>]*type="application/ld(?:&#x2b;|\+)json"[^>]*>(.*?)</script>`)

func (BuiltIn) Enrich(ctx context.Context, h *Hints) ([]Field, error) {
	if h.BuiltInURL == "" {
		return nil, nil
	}
	body, err := httpGet(ctx, h.BuiltInURL, "text/html")
	if err != nil {
		return nil, err
	}
	org := findOrganization(string(body))
	if org == nil {
		return nil, nil
	}
	var out []Field
	add := func(key string, val any, detail string) {
		if val == nil || val == "" {
			return
		}
		out = append(out, Field{Key: key, Value: val, Source: "builtin", Detail: detail})
	}

	if s, _ := org["name"].(string); s != "" {
		add("name", s, "")
	}
	if fd := org["foundingDate"]; fd != nil {
		if y := yearOf(fd); y != 0 {
			add("founded", y, "")
		}
	}
	if ne, ok := org["numberOfEmployees"].(map[string]any); ok {
		if v := numOf(ne["value"]); v != 0 {
			add("employees", v, "")
		}
	}
	if addr, ok := org["address"].(map[string]any); ok {
		loc := joinNonEmpty(", ",
			str(addr["addressLocality"]), str(addr["addressRegion"]), str(addr["addressCountry"]))
		add("hq", loc, "")
	}
	if ind := org["industry"]; ind != nil {
		add("industry", industryList(ind), "")
	}
	if s, _ := org["url"].(string); s != "" {
		add("website", s, "")
	}
	if s, _ := org["description"].(string); s != "" {
		add("description", strings.TrimSpace(s), "")
	}
	return out, nil
}

// findOrganization returns the first Organization/Corporation JSON-LD node.
func findOrganization(doc string) map[string]any {
	for _, m := range ldRe.FindAllStringSubmatch(doc, -1) {
		var v any
		if json.Unmarshal([]byte(strings.TrimSpace(m[1])), &v) != nil {
			continue
		}
		var nodes []map[string]any
		collect(v, &nodes)
		for _, n := range nodes {
			if t, _ := n["@type"].(string); t == "Organization" || t == "Corporation" {
				return n
			}
		}
	}
	return nil
}

func collect(v any, out *[]map[string]any) {
	switch x := v.(type) {
	case map[string]any:
		if g, ok := x["@graph"]; ok {
			collect(g, out)
			return
		}
		*out = append(*out, x)
	case []any:
		for _, e := range x {
			collect(e, out)
		}
	}
}

// ---- small coercers ----

func str(v any) string { s, _ := v.(string); return s }

// yearOf extracts a 4-digit year from a number or a date string.
func yearOf(v any) int {
	switch x := v.(type) {
	case float64:
		return int(x)
	case string:
		if len(x) >= 4 {
			if y, err := strconv.Atoi(x[:4]); err == nil {
				return y
			}
		}
	}
	return 0
}

func numOf(v any) int {
	switch x := v.(type) {
	case float64:
		return int(x)
	case string:
		if n, err := strconv.Atoi(strings.ReplaceAll(x, ",", "")); err == nil {
			return n
		}
	}
	return 0
}

func industryList(v any) any {
	switch x := v.(type) {
	case string:
		return x
	case []any:
		var out []string
		for _, e := range x {
			if s, ok := e.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func joinNonEmpty(sep string, parts ...string) string {
	var kept []string
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, sep)
}
