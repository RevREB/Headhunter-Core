// Package engine contains the core matching and de-duplication primitives for
// Headhunter-Core.
package engine

import (
	"hash/fnv"
	"math/bits"
	"strings"
	"unicode"
)

// SimHash computes a 64-bit SimHash fingerprint of text for near-duplicate
// detection of job descriptions.
//
// The text is lowercased and tokenized on Unicode whitespace and punctuation.
// Each distinct token is hashed to 64 bits with FNV-1a. For every one of the
// 64 bit positions we accumulate a signed vote weighted by the token's
// frequency: +weight when the token hash has that bit set, otherwise -weight.
// The final fingerprint has a 1 in each position whose accumulated vote is
// positive.
//
// Similar texts differ in only a few fingerprint bits, so a small Hamming
// distance between two fingerprints indicates the source texts are near
// duplicates. The empty string (and any text with no tokens) hashes to 0.
func SimHash(text string) uint64 {
	// Tally token frequencies so repeated terms carry proportional weight.
	freqs := make(map[string]int)
	for _, tok := range tokenize(text) {
		freqs[tok]++
	}

	// One signed accumulator per bit position.
	var counts [64]int
	for tok, weight := range freqs {
		h := hashToken(tok)
		for i := 0; i < 64; i++ {
			if h&(uint64(1)<<uint(i)) != 0 {
				counts[i] += weight
			} else {
				counts[i] -= weight
			}
		}
	}

	// Collapse the accumulators into the final fingerprint.
	var fingerprint uint64
	for i := 0; i < 64; i++ {
		if counts[i] > 0 {
			fingerprint |= uint64(1) << uint(i)
		}
	}
	return fingerprint
}

// Hamming returns the Hamming distance between two fingerprints: the number of
// bit positions at which they differ.
func Hamming(a, b uint64) int {
	return bits.OnesCount64(a ^ b)
}

// NearDuplicate reports whether two fingerprints are within maxDistance bits of
// one another. A maxDistance of 0 requires an exact match.
func NearDuplicate(a, b uint64, maxDistance int) bool {
	return Hamming(a, b) <= maxDistance
}

// tokenize lowercases text and splits it into tokens on any Unicode whitespace
// or punctuation. Empty tokens are dropped.
func tokenize(text string) []string {
	return strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsPunct(r) || unicode.IsSymbol(r)
	})
}

// hashToken maps a token to a 64-bit value using FNV-1a.
func hashToken(tok string) uint64 {
	h := fnv.New64a()
	// Write on a string never returns an error, so it is safe to ignore.
	_, _ = h.Write([]byte(tok))
	return h.Sum64()
}
