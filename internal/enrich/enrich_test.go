package enrich

import (
	"context"
	"encoding/json"
	"testing"
)

const orgFixture = `<html><head>
<script type="application/ld&#x2B;json">
{"@context":"https://schema.org","@graph":[
 {"@type":"BreadcrumbList"},
 {"@type":"Organization","name":"Acme","foundingDate":1925,
  "numberOfEmployees":{"@type":"QuantitativeValue","value":100000},
  "address":{"@type":"PostalAddress","addressLocality":"Irving","addressRegion":"TX","addressCountry":"US"},
  "industry":["Cloud","Industrial"],"url":"https://www.acme.com/","description":"We make things."}
]}
</script></head></html>`

func TestFindOrganization(t *testing.T) {
	org := findOrganization(orgFixture)
	if org == nil {
		t.Fatal("expected an Organization node")
	}
	if org["name"] != "Acme" {
		t.Errorf("name=%v", org["name"])
	}
	if y := yearOf(org["foundingDate"]); y != 1925 {
		t.Errorf("founded=%d", y)
	}
	ne, _ := org["numberOfEmployees"].(map[string]any)
	if numOf(ne["value"]) != 100000 {
		t.Errorf("employees=%v", ne["value"])
	}
}

func TestATSInference(t *testing.T) {
	h := &Hints{Docs: []json.RawMessage{
		json.RawMessage(`{"url":"https://builtin.com/job/x/1"}`),
		json.RawMessage(`{"url":"https://boards.greenhouse.io/acme/jobs/9"}`),
	}}
	fields, _ := ATS{}.Enrich(context.Background(), h)
	if len(fields) != 1 || fields[0].Value != "greenhouse" {
		t.Fatalf("expected greenhouse, got %+v", fields)
	}
	if fields[0].Detail != "boards.greenhouse.io/acme" {
		t.Errorf("detail=%q", fields[0].Detail)
	}
}

// fakeEnricher returns a fixed field for testing the authority merge.
type fakeEnricher struct {
	name  string
	field Field
}

func (f fakeEnricher) Name() string { return f.name }
func (f fakeEnricher) Enrich(context.Context, *Hints) ([]Field, error) {
	return []Field{f.field}, nil
}

func TestAssembleAuthority(t *testing.T) {
	h := &Hints{Name: "Acme"}
	// builtin (rank 2) then sec_edgar (rank 4) both set "x"; edgar must win.
	prof, _ := Assemble(context.Background(), h, []Enricher{
		fakeEnricher{"builtin", Field{Key: "x", Value: "low", Source: "builtin"}},
		fakeEnricher{"sec_edgar", Field{Key: "x", Value: "high", Source: "sec_edgar"}},
	})
	if prof.Fields["x"].Value != "high" {
		t.Errorf("authority merge wrong: %+v", prof.Fields["x"])
	}
	// and lower authority must NOT override higher, regardless of order.
	prof2, _ := Assemble(context.Background(), h, []Enricher{
		fakeEnricher{"sec_edgar", Field{Key: "y", Value: "high", Source: "sec_edgar"}},
		fakeEnricher{"builtin", Field{Key: "y", Value: "low", Source: "builtin"}},
	})
	if prof2.Fields["y"].Value != "high" {
		t.Errorf("lower authority overrode higher: %+v", prof2.Fields["y"])
	}
}

func TestDeriveFlags(t *testing.T) {
	p := Profile{Fields: map[string]Field{
		"industry": {Key: "industry", Value: []any{"Gaming", "Casino"}, Source: "builtin"},
	}}
	flags := deriveFlags(p)
	if len(flags) != 1 || flags[0] != "casino" {
		t.Errorf("expected [casino], got %v", flags)
	}
}
