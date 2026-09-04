package engine

import "testing"

func TestCanTransition(t *testing.T) {
	tests := []struct {
		name string
		from Status
		to   Status
		want bool
	}{
		// Allowed forward transitions: evaluated.
		{"evaluated->applied", StatusEvaluated, StatusApplied, true},
		{"evaluated->skip", StatusEvaluated, StatusSkip, true},
		{"evaluated->discarded", StatusEvaluated, StatusDiscarded, true},
		{"evaluated->rejected", StatusEvaluated, StatusRejected, true},
		// Allowed: applied.
		{"applied->responded", StatusApplied, StatusResponded, true},
		{"applied->rejected", StatusApplied, StatusRejected, true},
		{"applied->discarded", StatusApplied, StatusDiscarded, true},
		// Allowed: responded.
		{"responded->interview", StatusResponded, StatusInterview, true},
		{"responded->rejected", StatusResponded, StatusRejected, true},
		{"responded->discarded", StatusResponded, StatusDiscarded, true},
		// Allowed: interview.
		{"interview->offer", StatusInterview, StatusOffer, true},
		{"interview->rejected", StatusInterview, StatusRejected, true},
		{"interview->discarded", StatusInterview, StatusDiscarded, true},
		// Allowed: offer.
		{"offer->hired", StatusOffer, StatusHired, true},
		{"offer->rejected", StatusOffer, StatusRejected, true},
		{"offer->discarded", StatusOffer, StatusDiscarded, true},

		// Disallowed: skipping funnel stages.
		{"evaluated->responded", StatusEvaluated, StatusResponded, false},
		{"evaluated->interview", StatusEvaluated, StatusInterview, false},
		{"applied->interview", StatusApplied, StatusInterview, false},
		{"responded->offer", StatusResponded, StatusOffer, false},
		{"applied->hired", StatusApplied, StatusHired, false},
		// Disallowed: backward moves.
		{"applied->evaluated", StatusApplied, StatusEvaluated, false},
		{"offer->interview", StatusOffer, StatusInterview, false},
		// Disallowed: skip only reachable from evaluated.
		{"applied->skip", StatusApplied, StatusSkip, false},
		// Disallowed: out of terminal states.
		{"hired->applied", StatusHired, StatusApplied, false},
		{"rejected->applied", StatusRejected, StatusApplied, false},
		{"discarded->applied", StatusDiscarded, StatusApplied, false},
		{"skip->applied", StatusSkip, StatusApplied, false},
		// Disallowed: self-transition.
		{"applied->applied", StatusApplied, StatusApplied, false},
		// Disallowed: unknown endpoints.
		{"unknown from", Status("bogus"), StatusApplied, false},
		{"unknown to", StatusApplied, Status("bogus"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CanTransition(tt.from, tt.to); got != tt.want {
				t.Errorf("CanTransition(%q, %q) = %v, want %v", tt.from, tt.to, got, tt.want)
			}
		})
	}
}

func TestParseStatus(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    Status
		wantErr bool
	}{
		{"lowercase", "applied", StatusApplied, false},
		{"uppercase", "APPLIED", StatusApplied, false},
		{"mixed case", "InTeRvIeW", StatusInterview, false},
		{"trims whitespace", "  hired  ", StatusHired, false},
		{"side-state skip", "SKIP", StatusSkip, false},
		{"unknown", "pending", "", true},
		{"empty", "", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseStatus(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseStatus(%q) = %q, nil; want error", tt.input, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseStatus(%q) unexpected error: %v", tt.input, err)
			}
			if got != tt.want {
				t.Errorf("ParseStatus(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestValid(t *testing.T) {
	valid := []Status{
		StatusEvaluated, StatusApplied, StatusResponded, StatusInterview,
		StatusOffer, StatusHired, StatusRejected, StatusDiscarded, StatusSkip,
	}
	for _, s := range valid {
		if !s.Valid() {
			t.Errorf("Valid(%q) = false, want true", s)
		}
	}
	for _, s := range []Status{"", "bogus", "APPLIED"} {
		if s.Valid() {
			t.Errorf("Valid(%q) = true, want false", s)
		}
	}
}

func TestTerminal(t *testing.T) {
	tests := []struct {
		status Status
		want   bool
	}{
		{StatusHired, true},
		{StatusRejected, true},
		{StatusDiscarded, true},
		{StatusSkip, true},
		{StatusEvaluated, false},
		{StatusApplied, false},
		{StatusResponded, false},
		{StatusInterview, false},
		{StatusOffer, false},
		{Status("bogus"), false},
	}
	for _, tt := range tests {
		if got := Terminal(tt.status); got != tt.want {
			t.Errorf("Terminal(%q) = %v, want %v", tt.status, got, tt.want)
		}
	}
}

func TestRank(t *testing.T) {
	tests := []struct {
		status Status
		want   int
	}{
		{StatusEvaluated, 0},
		{StatusApplied, 1},
		{StatusResponded, 2},
		{StatusInterview, 3},
		{StatusOffer, 4},
		{StatusHired, 5},
		{StatusSkip, -1},
		{StatusRejected, -1},
		{StatusDiscarded, -1},
		{Status("bogus"), -1},
	}
	for _, tt := range tests {
		if got := Rank(tt.status); got != tt.want {
			t.Errorf("Rank(%q) = %d, want %d", tt.status, got, tt.want)
		}
	}
}
