package enrich

import (
	"context"
	"regexp"
)

// ATS infers which applicant-tracking system a company uses by scanning its
// stored posting docs for known ATS hosts. This is the discovery signal: a
// company seen via the Built In aggregator whose apply links resolve to, say,
// Greenhouse is a candidate to add to the per-company Greenhouse scraper.
type ATS struct{}

func (ATS) Name() string { return "ats" }

type atsPattern struct {
	name string
	re   *regexp.Regexp
}

// Ordered; first match wins. Each captures the board host/slug for the Detail.
var atsPatterns = []atsPattern{
	{"greenhouse", regexp.MustCompile(`(?i)((?:job-)?boards(?:-api)?\.greenhouse\.io/[a-z0-9_-]+)`)},
	{"lever", regexp.MustCompile(`(?i)(jobs\.lever\.co/[a-z0-9_-]+)`)},
	{"ashby", regexp.MustCompile(`(?i)(jobs\.ashbyhq\.com/[a-z0-9_-]+)`)},
	{"workday", regexp.MustCompile(`(?i)([a-z0-9_-]+\.[a-z0-9]*\.?myworkdayjobs\.com)`)},
	{"smartrecruiters", regexp.MustCompile(`(?i)(jobs\.smartrecruiters\.com/[a-z0-9_-]+)`)},
	{"workable", regexp.MustCompile(`(?i)(apply\.workable\.com/[a-z0-9_-]+)`)},
}

func (ATS) Enrich(ctx context.Context, h *Hints) ([]Field, error) {
	for _, doc := range h.Docs {
		s := string(doc)
		for _, p := range atsPatterns {
			if m := p.re.FindStringSubmatch(s); m != nil {
				return []Field{{Key: "ats", Value: p.name, Source: "hh_derived", Detail: m[1]}}, nil
			}
		}
	}
	return nil, nil
}
