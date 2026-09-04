package engine

import (
	"reflect"
	"testing"
)

func TestTrustScore(t *testing.T) {
	tests := []struct {
		name      string
		signals   PostingSignals
		wantScore int
		wantFlags []string
	}{
		{
			// Fully-formed, fresh, richly-described posting. All bonuses fire
			// and the raw total (50+15+10+10+5+10+10 = 110) is clamped to 100.
			name: "strong posting clamps at 100",
			signals: PostingSignals{
				HasApplyURL:      true,
				HasSalary:        true,
				HasCompany:       true,
				HasLocation:      true,
				DescriptionLen:   1200,
				PostedWithinDays: 2,
			},
			wantScore: 100,
			wantFlags: []string{},
		},
		{
			// A middling-but-valid posting where nothing is missing but the
			// description is neither substantial nor thin and the age is
			// unknown. 50+15+10+10+5 = 90, no flags.
			name: "complete posting neutral description",
			signals: PostingSignals{
				HasApplyURL:      true,
				HasSalary:        true,
				HasCompany:       true,
				HasLocation:      true,
				DescriptionLen:   200,
				PostedWithinDays: -1,
			},
			wantScore: 90,
			wantFlags: []string{},
		},
		{
			// Weak posting: missing everything, thin copy, unknown age.
			// 50-10 (thin) = 40, with structural flags in deterministic order.
			name: "weak posting",
			signals: PostingSignals{
				HasApplyURL:      false,
				HasSalary:        false,
				HasCompany:       false,
				HasLocation:      false,
				DescriptionLen:   50,
				PostedWithinDays: -1,
			},
			wantScore: 40,
			wantFlags: []string{
				FlagMissingApplyURL,
				FlagNoSalary,
				FlagMissingCompany,
				FlagMissingLocation,
				FlagThinDescription,
			},
		},
		{
			// Stale + thin posting missing all structural fields. Exercises the
			// stale branch alongside the thin penalty:
			// 50 - 10 (thin) - 15 (stale) = 25, with all six flags emitted in
			// deterministic order.
			name: "stale thin posting",
			signals: PostingSignals{
				HasApplyURL:      false,
				HasSalary:        false,
				HasCompany:       false,
				HasLocation:      false,
				DescriptionLen:   10,
				PostedWithinDays: 90,
			},
			wantScore: 25,
			wantFlags: []string{
				FlagMissingApplyURL,
				FlagNoSalary,
				FlagMissingCompany,
				FlagMissingLocation,
				FlagThinDescription,
				FlagStale,
			},
		},
		{
			// Boundary: exactly substantialDescLen chars counts as substantial;
			// exactly freshMaxDays counts as fresh. 50+10+10 = 70.
			name: "boundary substantial and fresh",
			signals: PostingSignals{
				DescriptionLen:   substantialDescLen,
				PostedWithinDays: freshMaxDays,
			},
			wantScore: 70,
			wantFlags: []string{
				FlagMissingApplyURL,
				FlagNoSalary,
				FlagMissingCompany,
				FlagMissingLocation,
			},
		},
		{
			// Boundary: exactly thinDescLen is NOT thin (the check is strictly
			// less-than), and exactly staleMinDays is NOT stale. Neutral desc,
			// neutral age. 50 with only structural flags.
			name: "boundary not-thin not-stale",
			signals: PostingSignals{
				DescriptionLen:   thinDescLen,
				PostedWithinDays: staleMinDays,
			},
			wantScore: 50,
			wantFlags: []string{
				FlagMissingApplyURL,
				FlagNoSalary,
				FlagMissingCompany,
				FlagMissingLocation,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotScore, gotFlags := TrustScore(tt.signals)
			if gotScore != tt.wantScore {
				t.Errorf("score = %d, want %d", gotScore, tt.wantScore)
			}
			if !reflect.DeepEqual(gotFlags, tt.wantFlags) {
				t.Errorf("flags = %v, want %v", gotFlags, tt.wantFlags)
			}
		})
	}
}

// TestTrustScoreClampRange asserts the invariant that TrustScore never returns a
// value outside [minScore, maxScore] across a sweep of description lengths and
// ages, guaranteeing both clamp branches are safe.
func TestTrustScoreClampRange(t *testing.T) {
	for descLen := 0; descLen <= 2000; descLen += 137 {
		for age := -1; age <= 400; age += 37 {
			s, _ := TrustScore(PostingSignals{
				DescriptionLen:   descLen,
				PostedWithinDays: age,
			})
			if s < minScore || s > maxScore {
				t.Fatalf("score %d out of range for descLen=%d age=%d", s, descLen, age)
			}
		}
	}
}

// TestTrustScoreFlagsNeverNil documents that flags is a non-nil slice even for
// a perfect posting, so callers can range over it unconditionally.
func TestTrustScoreFlagsNeverNil(t *testing.T) {
	_, flags := TrustScore(PostingSignals{
		HasApplyURL:      true,
		HasSalary:        true,
		HasCompany:       true,
		HasLocation:      true,
		DescriptionLen:   500,
		PostedWithinDays: 1,
	})
	if flags == nil {
		t.Fatal("flags should be non-nil even when empty")
	}
	if len(flags) != 0 {
		t.Errorf("expected no flags for a perfect posting, got %v", flags)
	}
}
