package engine

import (
	"net/url"
	"strings"
)

// trackingParams holds query-parameter keys that carry no semantic meaning for
// deduplication — analytics and referral markers. Lookups are performed with a
// lowercased key, so every entry here is stored lowercased.
var trackingParams = map[string]struct{}{
	"utm_source":   {},
	"utm_medium":   {},
	"utm_campaign": {},
	"utm_term":     {},
	"utm_content":  {},
	"gh_src":       {},
	"ref":          {},
	"source":       {},
	"src":          {},
}

// NormalizeURL canonicalizes a URL so that cosmetically different but
// semantically identical URLs collapse to the same string, which is what the
// deduplication layer keys on.
//
// It lowercases the scheme and host, drops the default port for the scheme
// (:80 for http, :443 for https), removes the fragment, strips known tracking
// query parameters, keeps the remaining parameters sorted by key, and removes a
// trailing slash from the path (except the root "/").
//
// If the input cannot be parsed, or parses to something without a host (e.g. a
// bare path or an opaque string), the trimmed original is returned unchanged so
// callers never lose data they cannot canonicalize.
func NormalizeURL(raw string) string {
	trimmed := strings.TrimSpace(raw)

	u, err := url.Parse(trimmed)
	if err != nil || u.Host == "" {
		return trimmed
	}

	u.Scheme = strings.ToLower(u.Scheme)

	// Rebuild the host as a lowercased hostname plus a non-default port.
	// Using Hostname/Port keeps IPv6 literals intact (they are re-bracketed
	// below) and lets us compare the port against the scheme's default.
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	switch {
	case u.Scheme == "http" && port == "80":
		port = ""
	case u.Scheme == "https" && port == "443":
		port = ""
	}
	if strings.Contains(host, ":") { // IPv6 literal
		host = "[" + host + "]"
	}
	if port != "" {
		u.Host = host + ":" + port
	} else {
		u.Host = host
	}

	// Drop the fragment entirely.
	u.Fragment = ""
	u.RawFragment = ""

	// Strip tracking parameters, then re-encode. url.Values.Encode sorts by
	// key, which satisfies the "keep remaining params sorted by key" rule.
	if u.RawQuery != "" {
		q := u.Query()
		for key := range q {
			if _, bad := trackingParams[strings.ToLower(key)]; bad {
				delete(q, key)
			}
		}
		u.RawQuery = q.Encode()
	}

	// Strip a trailing slash from the path unless it is the root. Clearing
	// RawPath forces url.String to re-derive the escaped form from Path.
	if u.Path != "/" {
		u.Path = strings.TrimSuffix(u.Path, "/")
		u.RawPath = ""
	}

	return u.String()
}

// legalSuffixes holds trailing legal-entity tokens that are stripped from a
// company name before comparison. Entries are lowercased; multi-part tokens
// such as "l.l.c" keep their internal punctuation because the candidate token
// is only trimmed of surrounding punctuation before lookup.
var legalSuffixes = map[string]struct{}{
	"inc":         {},
	"llc":         {},
	"l.l.c":       {},
	"ltd":         {},
	"limited":     {},
	"corp":        {},
	"corporation": {},
	"co":          {},
	"gmbh":        {},
	"plc":         {},
	"ag":          {},
}

// suffixCutset is the set of punctuation/space characters trimmed from around a
// candidate suffix token and from the tail of the final result.
const suffixCutset = " \t.,;:-"

// NormalizeCompany canonicalizes a company name for deduplication. It trims the
// input, collapses internal runs of whitespace to single spaces, lowercases the
// result, strips a single trailing legal-suffix token (with any surrounding
// punctuation or commas), and trims leftover punctuation and space.
//
// This file owns NormalizeCompany for the engine package; other components call
// it rather than redefining their own.
func NormalizeCompany(name string) string {
	// Trim, collapse whitespace, lowercase.
	s := strings.ToLower(strings.Join(strings.Fields(name), " "))
	if s == "" {
		return ""
	}

	fields := strings.Fields(s)
	if len(fields) > 1 {
		// Only strip a suffix when there is a name left over, so a company
		// literally called "Co" or "Inc" survives.
		candidate := strings.Trim(fields[len(fields)-1], suffixCutset)
		if _, ok := legalSuffixes[candidate]; ok {
			fields = fields[:len(fields)-1]
			s = strings.Join(fields, " ")
		}
	}

	// Trim any punctuation/space left dangling after suffix removal.
	return strings.TrimRight(s, suffixCutset)
}
