// Package engine contains the scoring and evaluation logic for Headhunter-Core.
package engine

// PostingSignals captures the coarse, cheaply-derived facts about a scraped job
// posting. Every field is something the scraper can determine without deep
// semantic analysis, which keeps trust scoring fast and deterministic.
type PostingSignals struct {
	HasApplyURL      bool
	HasSalary        bool
	HasCompany       bool
	HasLocation      bool
	DescriptionLen   int
	PostedWithinDays int // age in days; -1 when the posting date is unknown
}

// Scoring constants. Kept as named values so the heuristic is easy to tune and
// so each adjustment reads clearly at its use site.
const (
	baseScore = 50 // every posting starts here, neither trusted nor distrusted

	// Structural completeness bonuses: a posting that gives us the fields a
	// human would expect from a legitimate listing earns trust.
	bonusApplyURL = 15 // a real apply URL is the strongest legitimacy signal
	bonusSalary   = 10 // disclosed compensation correlates with genuine reqs
	bonusCompany  = 10 // a named employer is table stakes for a real posting
	bonusLocation = 5  // a stated location is weaker but still corroborating

	// Description depth: substantial copy suggests a real, detailed req;
	// near-empty copy suggests a scrape artifact or low-effort spam.
	substantialDescLen = 400 // >= this many chars is treated as substantial
	thinDescLen        = 120 // < this many chars is treated as thin
	bonusSubstantial   = 10
	penaltyThin        = 10

	// Recency: fresh postings are more actionable and less likely to be
	// stale reposts; very old postings are penalized.
	freshMaxDays = 7  // 0..7 days old counts as fresh
	staleMinDays = 45 // strictly greater than this counts as stale
	bonusFresh   = 10
	penaltyStale = 15

	minScore = 0
	maxScore = 100
)

// Flag strings. Declared as constants so callers can compare against them
// without risking a typo, and so the emitted vocabulary is documented in one
// place.
const (
	FlagMissingApplyURL = "missing_apply_url"
	FlagNoSalary        = "no_salary"
	FlagMissingCompany  = "missing_company"
	FlagMissingLocation = "missing_location"
	FlagThinDescription = "thin_description"
	FlagStale           = "stale"
)

// TrustScore computes a heuristic trust score in the range [0,100] for a scraped
// posting, along with a set of advisory flags describing which trust signals are
// missing or negative. The score is purely additive from a neutral baseline of
// 50 and is clamped to [0,100] at the end.
//
// The returned flags are emitted in a fixed, deterministic order (the order of
// the checks below) so that callers, snapshots, and tests can rely on it. When
// no flags apply, a non-nil empty slice is returned.
func TrustScore(p PostingSignals) (score int, flags []string) {
	score = baseScore
	flags = make([]string, 0, 6)

	// Apply URL: reward its presence, flag its absence.
	if p.HasApplyURL {
		score += bonusApplyURL
	} else {
		flags = append(flags, FlagMissingApplyURL)
	}

	// Salary disclosure.
	if p.HasSalary {
		score += bonusSalary
	} else {
		flags = append(flags, FlagNoSalary)
	}

	// Named company.
	if p.HasCompany {
		score += bonusCompany
	} else {
		flags = append(flags, FlagMissingCompany)
	}

	// Stated location.
	if p.HasLocation {
		score += bonusLocation
	} else {
		flags = append(flags, FlagMissingLocation)
	}

	// Description depth: substantial copy is rewarded, thin copy is penalized
	// and flagged. Lengths between the two thresholds are treated as neutral.
	switch {
	case p.DescriptionLen >= substantialDescLen:
		score += bonusSubstantial
	case p.DescriptionLen < thinDescLen:
		score -= penaltyThin
		flags = append(flags, FlagThinDescription)
	}

	// Recency: only evaluated when the age is known (PostedWithinDays >= 0).
	// Fresh postings gain trust; stale ones lose it and are flagged.
	if p.PostedWithinDays >= 0 {
		switch {
		case p.PostedWithinDays <= freshMaxDays:
			score += bonusFresh
		case p.PostedWithinDays > staleMinDays:
			score -= penaltyStale
			flags = append(flags, FlagStale)
		}
	}

	// Clamp to the valid range.
	if score < minScore {
		score = minScore
	} else if score > maxScore {
		score = maxScore
	}

	return score, flags
}
