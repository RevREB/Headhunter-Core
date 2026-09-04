// Package operator is the operator-lite: it will create one Kubernetes Job per
// ATS from the git-declared scraper catalog (scale-to-zero via
// ttlSecondsAfterFinished). The client-go wiring lands in a follow-up; the
// current build exposes /api/cycle as an accepted-but-not-yet-dispatched stub.
package operator
