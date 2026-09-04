package engine

import (
	"strings"
	"unicode"
)

// dedupSeparator joins the normalized company and role halves of a dedup key.
// The spec fixes it as "::". Note that neither half is guaranteed to be free of
// this sequence (interior punctuation is preserved, so a role such as "a :: b"
// normalizes to "a :: b"); the separator is a formatting convention, not a
// parseable delimiter. Keys are compared whole, never split back apart.
const dedupSeparator = "::"

// DedupKey returns a canonical, stable identity for a posting built from its
// company and role. Two postings whose company and role are equivalent up to
// casing, whitespace, and surrounding punctuation collapse to the same key,
// making it suitable for deduplication.
//
// The key is NormalizeCompany(company) + "::" + normalizeRole(role).
// NormalizeCompany is provided elsewhere in this package.
//
// DedupKey is deterministic: identical inputs always yield identical output,
// and equivalent inputs yield equal output.
func DedupKey(company, role string) string {
	return NormalizeCompany(company) + dedupSeparator + normalizeRole(role)
}

// normalizeRole canonicalizes a role/title string. It lowercases the text,
// collapses every run of whitespace to a single ASCII space, and strips any
// leading or trailing punctuation and symbol runes. Interior punctuation is
// preserved (e.g. "c++", "front-end") because it can be semantically
// meaningful within a title.
//
// The result contains no leading or trailing spaces. An input that is empty or
// consists solely of whitespace/punctuation normalizes to the empty string.
func normalizeRole(role string) string {
	// Lowercase first so casing never affects any downstream comparison.
	role = strings.ToLower(role)

	// Collapse all whitespace (including tabs, newlines, and Unicode spaces)
	// into single spaces, trimming the ends. strings.Fields splits on any
	// unicode.IsSpace rune and discards empty fields, so this both collapses
	// runs and trims.
	role = strings.Join(strings.Fields(role), " ")

	// Strip the surrounding punctuation/symbol run, including any whitespace
	// interspersed within it, but keep interior punctuation. Folding whitespace
	// into the predicate lets a single pass reduce mixed edges like ". : foo"
	// or "foo ! ?" all the way to "foo". A predicate that stopped at the first
	// space would leave a stray leading/trailing punctuation token behind,
	// breaking the promise that inputs equivalent up to surrounding punctuation
	// and whitespace produce equal keys. Interior single spaces are untouched
	// because TrimFunc only removes runes at the ends.
	return strings.TrimFunc(role, isTrimmableEdgeRune)
}

// isTrimmableEdgeRune reports whether r should be removed when it appears at the
// leading or trailing edge of a role string. Punctuation, symbol, and space
// runes qualify; letters and digits do not.
func isTrimmableEdgeRune(r rune) bool {
	return unicode.IsPunct(r) || unicode.IsSymbol(r) || unicode.IsSpace(r)
}
