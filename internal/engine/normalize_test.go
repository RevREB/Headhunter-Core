package engine

import "testing"

func TestNormalizeURL(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"lowercase scheme and host", "HTTP://Example.COM/Path", "http://example.com/Path"},
		{"drop default http port", "http://example.com:80/path", "http://example.com/path"},
		{"drop default https port", "https://example.com:443/path", "https://example.com/path"},
		{"keep non-default port", "https://example.com:8443/path", "https://example.com:8443/path"},
		{"remove fragment", "https://example.com/jobs#section", "https://example.com/jobs"},
		{"strip tracking params", "https://example.com/jobs?utm_source=x&utm_medium=y&id=42", "https://example.com/jobs?id=42"},
		{"tracking param case-insensitive", "https://example.com/j?UTM_Source=x&Ref=y&q=1", "https://example.com/j?q=1"},
		{"sort remaining params", "https://example.com/j?b=2&a=1&c=3", "https://example.com/j?a=1&b=2&c=3"},
		{"all params stripped leaves clean url", "https://example.com/j?ref=abc&src=def", "https://example.com/j"},
		{"strip trailing slash", "https://example.com/jobs/", "https://example.com/jobs"},
		{"keep root slash", "https://example.com/", "https://example.com/"},
		{"no path", "https://example.com", "https://example.com"},
		{"combined", "HTTPS://Example.com:443/Careers/?gh_src=abc&Role=SRE#apply", "https://example.com/Careers?Role=SRE"},
		{"unparseable returns trimmed original", "   ://not a url   ", "://not a url"},
		{"no host returns trimmed original", "/relative/path?a=1", "/relative/path?a=1"},
		{"opaque returns trimmed original", "mailto:jobs@example.com", "mailto:jobs@example.com"},
		{"empty string", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NormalizeURL(tt.in); got != tt.want {
				t.Errorf("NormalizeURL(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestNormalizeCompany(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"trim and lowercase", "  Acme  ", "acme"},
		{"collapse whitespace", "Acme\t  Widgets\nCo", "acme widgets"},
		{"strip inc", "Acme Inc", "acme"},
		{"strip inc with comma", "Acme, Inc.", "acme"},
		{"strip llc", "Foo Bar LLC", "foo bar"},
		{"strip dotted llc", "Foo Bar L.L.C.", "foo bar"},
		{"strip corporation", "Globex Corporation", "globex"},
		{"strip corp", "Globex Corp.", "globex"},
		{"strip ltd", "Widgets Ltd", "widgets"},
		{"strip limited", "Widgets Limited", "widgets"},
		{"strip gmbh", "Beispiel GmbH", "beispiel"},
		{"strip plc", "Example PLC", "example"},
		{"strip ag", "Muster AG", "muster"},
		{"strip co", "Trading Co.", "trading"},
		{"no suffix untouched", "Netflix", "netflix"},
		{"single-token suffix kept", "Inc", "inc"},
		{"suffix mid-name not stripped", "Inc Magazine", "inc magazine"},
		{"leftover punctuation trimmed", "Acme -", "acme"},
		{"empty string", "   ", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NormalizeCompany(tt.in); got != tt.want {
				t.Errorf("NormalizeCompany(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
