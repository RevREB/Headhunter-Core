package api

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/RevREB/Headhunter-Core/internal/store"
)

func TestStripHTML(t *testing.T) {
	cases := map[string]string{
		"<p>Hello&nbsp;<b>World</b></p>": "Hello World",
		"line1<br>line2":                "line1 line2",
		"  spaced   out  ":              "spaced out",
		"":                              "",
		"Own the <a href=x>platform</a> &amp; tooling": "Own the platform & tooling",
	}
	for in, want := range cases {
		if got := stripHTML(in); got != want {
			t.Errorf("stripHTML(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestExtractDescription(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{`{"content":"<p>Build <b>systems</b></p>","title":"x"}`, "Build systems"},
		{`{"descriptionPlain":"Own the platform.","note":"hi"}`, "Own the platform."},
		{`{"jobDescription":"Run SRE.","summary":"short"}`, "Run SRE."}, // longest description-like wins
		{`{"title":"Engineer","location":"Remote"}`, ""},               // no description-like field
		{`"just a string"`, ""},                                        // raw not an object
		{``, ""},                                                       // empty
	}
	for _, c := range cases {
		if got := extractDescription(json.RawMessage(c.raw)); got != c.want {
			t.Errorf("extractDescription(%s) = %q, want %q", c.raw, got, c.want)
		}
	}
}

func TestJobContext(t *testing.T) {
	a := store.Application{Company: "Acme", Role: "Staff SRE"}
	doc := json.RawMessage(`{"location":"Remote (US)","comp":"$250k","raw":{"content":"Operate <b>Kubernetes</b> at scale."}}`)
	got := jobContext(a, doc)
	for _, want := range []string{"Company: Acme", "Role: Staff SRE", "Location: Remote (US)", "Compensation: $250k", "Operate Kubernetes at scale."} {
		if !strings.Contains(got, want) {
			t.Errorf("jobContext missing %q in:\n%s", want, got)
		}
	}
	// nil doc -> title/company only, no Location line
	if bare := jobContext(a, nil); strings.Contains(bare, "Location:") || !strings.Contains(bare, "Company: Acme") {
		t.Errorf("jobContext(nil doc) unexpected: %q", bare)
	}
}
