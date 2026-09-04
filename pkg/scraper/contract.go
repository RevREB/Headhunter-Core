// Package scraper defines the Headhunter ATS scraper wire contract (v1).
//
// Core owns this package; individual scrapers in RevREB/Headhunter-Scrapers
// import it. The contract is deliberately tiny: a scraper's ONLY job is to
// fetch raw postings from one ATS. Everything durable and valuable — dedup,
// URL normalization, SimHash fingerprinting, trust scoring, repost detection,
// normalization, persistence, analytics — lives in Core, never in a scraper.
package scraper

import "encoding/json"

// ContractVersion is the semantic version of the scraper wire contract.
// Core validates a scraper's announced version against this and supports N-1.
const ContractVersion = "1.0.0"

// PortalConfig is the input a scraper receives (via env/args) describing which
// ATS to fetch and with what query. CredsRef points at a secret reference,
// never the secret value itself.
type PortalConfig struct {
	ATS       string            `json:"ats"`
	Endpoints map[string]string `json:"endpoints,omitempty"`
	Query     map[string]any    `json:"query,omitempty"`
	Filters   map[string]any    `json:"filters,omitempty"`
	CredsRef  string            `json:"credsRef,omitempty"`
}

// RawPosting is the single output type every scraper emits. Fields beyond the
// core identity go into Raw as the ATS-native blob; Core decides what to keep.
type RawPosting struct {
	URL      string          `json:"url"`
	Title    string          `json:"title"`
	Company  string          `json:"company"`
	Location string          `json:"location,omitempty"`
	Comp     string          `json:"comp,omitempty"`
	PostedAt string          `json:"postedAt,omitempty"` // RFC3339
	Raw      json.RawMessage `json:"raw,omitempty"`
}

// Handshake is printed by a scraper as its FIRST line of stdout, so Core can
// validate compatibility before trusting any output that follows.
type Handshake struct {
	ATS             string   `json:"ats"`
	ContractVersion string   `json:"contractVersion"`
	Capabilities    []string `json:"capabilities,omitempty"`
}
