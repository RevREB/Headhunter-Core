// Package engine implements the core logic of the Headhunter application
// tracker. This file defines the application-status state machine: the set of
// valid statuses, their funnel ordering, and the transitions permitted between
// them.
package engine

import (
	"fmt"
	"strings"
)

// Status is an application's position in the hiring funnel. It is stored as a
// lowercase string so that persisted values are human-readable and stable.
type Status string

// The full set of statuses. The first six form the linear funnel
// (evaluated -> ... -> hired); the remaining three are terminal side-states an
// application can drop into.
const (
	StatusEvaluated Status = "evaluated"
	StatusApplied   Status = "applied"
	StatusResponded Status = "responded"
	StatusInterview Status = "interview"
	StatusOffer     Status = "offer"
	StatusHired     Status = "hired"

	StatusRejected  Status = "rejected"
	StatusDiscarded Status = "discarded"
	StatusSkip      Status = "skip"
	StatusInbox     Status = "inbox"
)

// funnelRank maps the progression statuses to their 0..5 funnel position.
// Side-states are deliberately absent (Rank reports -1 for them).
var funnelRank = map[Status]int{
	StatusEvaluated: 0,
	StatusApplied:   1,
	StatusResponded: 2,
	StatusInterview: 3,
	StatusOffer:     4,
	StatusHired:     5,
}

// terminalStatuses are the statuses with no outgoing transitions.
var terminalStatuses = map[Status]bool{
	StatusHired:     true,
	StatusRejected:  true,
	StatusDiscarded: true,
	StatusSkip:      true,
}

// transitions is the adjacency set of allowed forward moves, keyed by the
// source status. A status absent from this map (or mapping to an empty set) has
// no outgoing edges.
var transitions = map[Status]map[Status]bool{
	StatusInbox: {
		StatusEvaluated: true,
		StatusApplied:   true,
		StatusSkip:      true,
		StatusDiscarded: true,
	},
	StatusEvaluated: {
		StatusApplied:   true,
		StatusSkip:      true,
		StatusDiscarded: true,
		StatusRejected:  true,
	},
	StatusApplied: {
		StatusResponded: true,
		StatusRejected:  true,
		StatusDiscarded: true,
	},
	StatusResponded: {
		StatusInterview: true,
		StatusRejected:  true,
		StatusDiscarded: true,
	},
	StatusInterview: {
		StatusOffer:     true,
		StatusRejected:  true,
		StatusDiscarded: true,
	},
	StatusOffer: {
		StatusHired:     true,
		StatusRejected:  true,
		StatusDiscarded: true,
	},
}

// allStatuses is the authoritative set of known statuses, used by Valid and
// ParseStatus.
var allStatuses = map[Status]bool{
	StatusInbox:     true,
	StatusEvaluated: true,
	StatusApplied:   true,
	StatusResponded: true,
	StatusInterview: true,
	StatusOffer:     true,
	StatusHired:     true,
	StatusRejected:  true,
	StatusDiscarded: true,
	StatusSkip:      true,
}

// ParseStatus converts a string to a Status. Matching is case-insensitive and
// surrounding whitespace is trimmed. It returns an error for any unknown value.
func ParseStatus(s string) (Status, error) {
	candidate := Status(strings.ToLower(strings.TrimSpace(s)))
	if allStatuses[candidate] {
		return candidate, nil
	}
	return "", fmt.Errorf("engine: unknown status %q", s)
}

// Valid reports whether s is one of the known statuses.
func (s Status) Valid() bool {
	return allStatuses[s]
}

// Rank returns the funnel position (0..5) for a progression status, or -1 for
// the side-states (skip, rejected, discarded) and for any unknown status.
func Rank(s Status) int {
	if r, ok := funnelRank[s]; ok {
		return r
	}
	return -1
}

// Terminal reports whether s is a terminal status (no outgoing transitions).
// Unknown statuses are not terminal.
func Terminal(s Status) bool {
	return terminalStatuses[s]
}

// CanTransition reports whether moving an application from status from to status
// to is permitted. Any unknown or invalid endpoint yields false, as does any
// pair not present in the transition table (including self-transitions).
func CanTransition(from, to Status) bool {
	if !from.Valid() || !to.Valid() {
		return false
	}
	return transitions[from][to]
}
