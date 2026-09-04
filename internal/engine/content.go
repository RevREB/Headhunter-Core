package engine

import (
	"encoding/json"
	"html"
	"strings"
)

// ExtractDescription best-effort pulls a job description out of an ATS raw
// record: the longest string field whose key looks like a description/content/
// body, HTML-stripped. Returns "" when the raw record has no such field.
func ExtractDescription(raw json.RawMessage) string {
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

// stripHTML removes tags, unescapes entities, and collapses whitespace. It
// unescapes first so entity-encoded markup (e.g. greenhouse's "&lt;div&gt;")
// is turned into tags and then stripped.
func stripHTML(s string) string {
	if s == "" {
		return ""
	}
	s = html.UnescapeString(s)
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
	return strings.Join(strings.Fields(b.String()), " ")
}

// ContentFingerprint is the dedup/repost fingerprint: a SimHash over the title
// plus the extracted JD description. It deliberately ignores volatile metadata
// (per-listing ids, urls, location fields) so the same role posted in many
// locations, or reposted, collapses to a near-identical fingerprint.
func ContentFingerprint(title string, raw json.RawMessage) uint64 {
	return SimHash(title + " " + ExtractDescription(raw))
}
