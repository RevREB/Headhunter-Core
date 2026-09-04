package importer

import "testing"

func TestParseTracker(t *testing.T) {
	md := "# Applications Tracker\n\nSome preamble text.\n\n" +
		"| # | Date | Company | Role | Score | Status | PDF | Report | Notes |\n" +
		"|---|------|---------|------|-------|--------|-----|--------|-------|\n" +
		"| 1 | 2026-08-20 | Acme | SRE Lead | 4.2/5 | Applied | | [r](reports/1.md) | hi |\n" +
		"| 2 | 2026-08-19 | Globex | Platform Eng | — | Discarded | | | |\n" +
		"| 3 | | Initech | Manager | 3/5 | SKIP | | | |\n" +
		"\nTrailing prose after the table.\n"
	rows := ParseTracker(md)
	if len(rows) != 3 {
		t.Fatalf("got %d rows, want 3", len(rows))
	}
	if rows[0].Company != "Acme" || rows[0].Role != "SRE Lead" || rows[0].Status != "applied" {
		t.Errorf("row0 = %+v", rows[0])
	}
	if rows[0].Score == nil || *rows[0].Score != 4.2 {
		t.Errorf("row0 score = %v", rows[0].Score)
	}
	if !rows[0].HasDate {
		t.Errorf("row0 should have a date")
	}
	if rows[1].Status != "discarded" || rows[1].Score != nil {
		t.Errorf("row1 = %+v", rows[1])
	}
	if rows[2].Status != "skip" || rows[2].HasDate {
		t.Errorf("row2 = %+v", rows[2])
	}
}
