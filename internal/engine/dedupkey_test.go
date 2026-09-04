package engine

import (
	"strings"
	"testing"
)

func TestNormalizeRole(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"already canonical", "software engineer", "software engineer"},
		{"lowercases", "Software Engineer", "software engineer"},
		{"collapses internal whitespace", "senior   staff\tengineer", "senior staff engineer"},
		{"trims surrounding whitespace", "  platform engineer  ", "platform engineer"},
		{"collapses newlines", "site\nreliability\r\nengineer", "site reliability engineer"},
		{"strips leading punctuation", "***Senior Engineer", "senior engineer"},
		{"strips trailing punctuation", "Engineer!!!", "engineer"},
		{"strips surrounding brackets", "[Lead Architect]", "lead architect"},
		{"preserves interior punctuation", "Front-End Developer", "front-end developer"},
		{"preserves interior plus signs", "C++ Developer", "c++ developer"},
		{"strips punctuation then whitespace", "- devops -", "devops"},
		{"only punctuation", "***", ""},
		{"only whitespace", "   \t\n", ""},
		{"multiple ascii spaces collapse", "data  scientist", "data scientist"},
		{"unicode nbsp space collapses", "data scientist", "data scientist"},
		{"parenthetical kept interior", "engineer (backend)", "engineer (backend"},
		{"strips mixed leading punct and space", ". : devops", "devops"},
		{"strips mixed trailing punct and space", "devops ! ?", "devops"},
		{"strips mixed surrounding punct and space", "!! ? staff engineer -- ", "staff engineer"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizeRole(tt.in); got != tt.want {
				t.Errorf("normalizeRole(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestDedupKey(t *testing.T) {
	// Assumes NormalizeCompany is defined elsewhere in the package. These
	// cases exercise DedupKey's structure and its use of normalizeRole on the
	// role half; the exact company normalization is covered by NormalizeCompany's
	// own tests.
	tests := []struct {
		name         string
		company      string
		role         string
		wantContains string // separator and normalized role half
	}{
		{
			name:         "basic",
			company:      "Acme Corp",
			role:         "Software Engineer",
			wantContains: dedupSeparator + "software engineer",
		},
		{
			name:         "role normalized",
			company:      "Acme Corp",
			role:         "  Senior   Engineer!! ",
			wantContains: dedupSeparator + "senior engineer",
		},
		{
			name:         "empty role",
			company:      "Acme Corp",
			role:         "***",
			wantContains: dedupSeparator + "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DedupKey(tt.company, tt.role)
			if !strings.Contains(got, dedupSeparator) {
				t.Fatalf("DedupKey(%q, %q) = %q, missing separator %q", tt.company, tt.role, got, dedupSeparator)
			}
			if !strings.HasSuffix(got, tt.wantContains) {
				t.Errorf("DedupKey(%q, %q) = %q, want suffix %q", tt.company, tt.role, got, tt.wantContains)
			}
			wantPrefix := NormalizeCompany(tt.company) + dedupSeparator
			if !strings.HasPrefix(got, wantPrefix) {
				t.Errorf("DedupKey(%q, %q) = %q, want prefix %q", tt.company, tt.role, got, wantPrefix)
			}
		})
	}
}

func TestDedupKeyDeterministicAndEquivalent(t *testing.T) {
	// Same inputs must be stable.
	a := DedupKey("Acme Corp", "Software Engineer")
	b := DedupKey("Acme Corp", "Software Engineer")
	if a != b {
		t.Errorf("DedupKey not deterministic: %q != %q", a, b)
	}

	// Equivalent role inputs must collapse to the same key.
	c := DedupKey("Acme Corp", "  software   engineer  ")
	d := DedupKey("Acme Corp", "Software Engineer")
	if c != d {
		t.Errorf("equivalent roles produced different keys: %q != %q", c, d)
	}
}
