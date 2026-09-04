package api

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/RevREB/Headhunter-Core/internal/store"
)

func TestJobContext(t *testing.T) {
	a := store.Application{Company: "Acme", Role: "Staff SRE"}
	doc := json.RawMessage(`{"location":"Remote (US)","comp":"$250k","raw":{"content":"Operate <b>Kubernetes</b> at scale."}}`)
	got := jobContext(a, doc)
	for _, want := range []string{"Company: Acme", "Role: Staff SRE", "Location: Remote (US)", "Compensation: $250k", "Operate Kubernetes at scale."} {
		if !strings.Contains(got, want) {
			t.Errorf("jobContext missing %q in:\n%s", want, got)
		}
	}
	if bare := jobContext(a, nil); strings.Contains(bare, "Location:") || !strings.Contains(bare, "Company: Acme") {
		t.Errorf("jobContext(nil doc) unexpected: %q", bare)
	}
}
