package api

import "testing"

func TestResolveApplyURL(t *testing.T) {
	cases := map[string]string{
		"https://jobs.lever.co/acme/123":             "https://jobs.lever.co/acme/123/apply",
		"https://jobs.lever.co/acme/123/apply":       "https://jobs.lever.co/acme/123/apply",
		"https://jobs.ashbyhq.com/acme/123":          "https://jobs.ashbyhq.com/acme/123/application",
		"https://boards.greenhouse.io/acme/jobs/123": "https://boards.greenhouse.io/acme/jobs/123",
		"https://builtin.com/job/x/1":                "https://builtin.com/job/x/1",
	}
	for in, want := range cases {
		if got := resolveApplyURL(in); got != want {
			t.Errorf("resolveApplyURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFindApplyControl(t *testing.T) {
	snap := `- button "Applied filters" [ref=e2]
- link "Apply for this job" [ref=e7]
- button "Save" [ref=e9]`
	name, ref, ok := findApplyControl(snap)
	if !ok || ref != "e7" {
		t.Fatalf("got name=%q ref=%q ok=%v; want the Apply link e7", name, ref, ok)
	}
	// "Applied filters" must NOT be treated as an apply control.
	if _, _, ok := findApplyControl(`- button "Applied filters" [ref=e2]`); ok {
		t.Error("matched 'Applied filters' as an apply control")
	}
}

func TestMatchValueSafety(t *testing.T) {
	p := applyProfile{Name: "Richard Baker", Email: "r@x.com", Phone: "555-1212",
		Address: "1 A St, Reno, NV 89501", LinkedIn: "https://lnkd/x"}
	// safe fills
	if v, _ := matchValue("First Name", p); v != "Richard" {
		t.Errorf("first name = %q", v)
	}
	if v, _ := matchValue("Email Address", p); v != "r@x.com" {
		t.Errorf("email = %q", v)
	}
	if v, _ := matchValue("City", p); v != "Reno" {
		t.Errorf("city = %q", v)
	}
	// hard-excluded fields must never be filled
	for _, bad := range []string{
		"Are you authorized to work in the US?",
		"Will you require sponsorship?",
		"Gender", "Race/Ethnicity", "Veteran status",
		"Cover letter", "Desired salary", "Why do you want to work here?",
	} {
		if _, ok := matchValue(bad, p); ok {
			t.Errorf("must NOT auto-fill %q", bad)
		}
	}
}

func TestExtractReportURL(t *testing.T) {
	caci := "# Evaluation: CACI — AI Enterprise Architect\n\n**URL:** https://caci.wd1.myworkdayjobs.com/External/job/Washington-DC-US/AI-Enterprise-Architect_331405\n**Via:** — (direct)\n"
	if got := extractReportURL(caci); got != "https://caci.wd1.myworkdayjobs.com/External/job/Washington-DC-US/AI-Enterprise-Architect_331405" {
		t.Errorf("labeled URL: got %q", got)
	}
	yaml := "no label here\n```yaml\ncompany: X\nurl: https://boards.greenhouse.io/x/jobs/9\n```\n"
	if got := extractReportURL(yaml); got != "https://boards.greenhouse.io/x/jobs/9" {
		t.Errorf("yaml url: got %q", got)
	}
	// trailing punctuation trimmed; builtin/localhost skipped in fallback
	if got := extractReportURL("see https://jobs.lever.co/acme/123)."); got != "https://jobs.lever.co/acme/123" {
		t.Errorf("fallback trim: got %q", got)
	}
	if got := extractReportURL("Tool @ 127.0.0.1 and https://builtin.com/job/x/1 only"); got != "" {
		t.Errorf("should skip builtin/localhost-only: got %q", got)
	}
	if got := extractReportURL("no urls at all"); got != "" {
		t.Errorf("none: got %q", got)
	}
}
