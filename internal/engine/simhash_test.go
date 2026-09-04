package engine

import (
	"math/bits"
	"testing"
)

const jobA = `Senior Site Reliability Engineer. We are looking for an experienced SRE
to own our Kubernetes platform, build resilient infrastructure, and lead
incident response. Strong background in Go, Terraform, and observability is
required. Fully remote position.`

// jobB is jobA with a single word changed ("Senior" -> "Staff").
const jobB = `Staff Site Reliability Engineer. We are looking for an experienced SRE
to own our Kubernetes platform, build resilient infrastructure, and lead
incident response. Strong background in Go, Terraform, and observability is
required. Fully remote position.`

// jobC shares no meaningful vocabulary with jobA.
const jobC = `Pastry chef wanted for a busy downtown bakery. Must love croissants,
sourdough, and early mornings. Experience decorating wedding cakes preferred.
Weekend availability essential. Competitive hourly pay plus tips.`

func TestSimHashIdentical(t *testing.T) {
	a := SimHash(jobA)
	b := SimHash(jobA)
	if a != b {
		t.Fatalf("identical text produced different fingerprints: %016x vs %016x", a, b)
	}
	if d := Hamming(a, b); d != 0 {
		t.Fatalf("identical text Hamming distance = %d, want 0", d)
	}
}

func TestSimHashDeterministic(t *testing.T) {
	// Determinism across many texts, including tricky inputs.
	for _, s := range []string{"", "   ", "!!!", "one", "one two three", jobA, jobC} {
		if SimHash(s) != SimHash(s) {
			t.Errorf("SimHash not deterministic for %q", s)
		}
	}
}

func TestSimHashEmptyIsZero(t *testing.T) {
	if got := SimHash(""); got != 0 {
		t.Errorf("SimHash(\"\") = %016x, want 0", got)
	}
	// Text that tokenizes to nothing must behave like the empty string.
	if got := SimHash("  ---  ...  "); got != 0 {
		t.Errorf("SimHash(punctuation only) = %016x, want 0", got)
	}
}

func TestSmallChangeSmallDistance(t *testing.T) {
	a := SimHash(jobA)
	b := SimHash(jobB)
	d := Hamming(a, b)
	if d == 0 {
		t.Fatalf("one-word change produced identical fingerprints; expected a small non-zero distance")
	}
	if d > 12 {
		t.Fatalf("one-word change distance = %d, want a small distance (<=12)", d)
	}
}

func TestDisjointTextsLargeDistance(t *testing.T) {
	a := SimHash(jobA)
	c := SimHash(jobC)
	near := Hamming(a, SimHash(jobB)) // near-duplicate distance for comparison
	far := Hamming(a, c)
	if far <= near {
		t.Fatalf("disjoint texts distance (%d) should exceed near-duplicate distance (%d)", far, near)
	}
	if far < 20 {
		t.Fatalf("disjoint texts distance = %d, want a large distance (>=20)", far)
	}
}

func TestTokenizationCaseAndPunctuation(t *testing.T) {
	// Case and surrounding punctuation must not affect the fingerprint.
	if SimHash("Go, Terraform; Kubernetes!") != SimHash("go terraform kubernetes") {
		t.Errorf("case/punctuation changed the fingerprint")
	}
}

func TestHamming(t *testing.T) {
	cases := []struct {
		a, b uint64
		want int
	}{
		{0, 0, 0},
		{0xFFFFFFFFFFFFFFFF, 0, 64},
		{0b1011, 0b0001, 2},
		{0xF0, 0x0F, 8},
	}
	for _, c := range cases {
		if got := Hamming(c.a, c.b); got != c.want {
			t.Errorf("Hamming(%016x, %016x) = %d, want %d", c.a, c.b, got, c.want)
		}
		// Cross-check against a direct popcount of the XOR.
		if got := Hamming(c.a, c.b); got != bits.OnesCount64(c.a^c.b) {
			t.Errorf("Hamming disagrees with OnesCount64 for %016x,%016x", c.a, c.b)
		}
	}
}

func TestHammingSymmetric(t *testing.T) {
	a := SimHash(jobA)
	b := SimHash(jobC)
	if Hamming(a, b) != Hamming(b, a) {
		t.Errorf("Hamming is not symmetric")
	}
}

func TestNearDuplicate(t *testing.T) {
	a := SimHash(jobA)
	b := SimHash(jobB)
	c := SimHash(jobC)

	d := Hamming(a, b)
	if !NearDuplicate(a, b, d) {
		t.Errorf("NearDuplicate should be true at exactly the measured distance %d", d)
	}
	if NearDuplicate(a, b, d-1) {
		t.Errorf("NearDuplicate should be false below the measured distance %d", d)
	}
	if !NearDuplicate(a, a, 0) {
		t.Errorf("a fingerprint must be a near-duplicate of itself at maxDistance 0")
	}
	if NearDuplicate(a, c, 5) {
		t.Errorf("disjoint texts should not be near-duplicates within 5 bits")
	}
}
