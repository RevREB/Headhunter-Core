// Package importer parses the career-ops flat-file tracker into Headhunter rows.
// It reads the format as DATA (a markdown table); it shares no upstream code.
package importer

import (
	"bufio"
	"strconv"
	"strings"
	"time"
)

// TrackerRow is one parsed application from applications.md.
type TrackerRow struct {
	Num     int
	Date    time.Time
	HasDate bool
	Company string
	Role    string
	Score   *float64
	Status  string // normalized to the Headhunter status enum
}

var knownStatus = map[string]bool{
	"evaluated": true, "applied": true, "responded": true, "interview": true,
	"offer": true, "hired": true, "rejected": true, "discarded": true, "skip": true,
}

func normStatus(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if knownStatus[s] {
		return s
	}
	return "evaluated" // unknown/blank -> treat as evaluated
}

func parseScore(s string) *float64 {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '/'); i > 0 { // "3.3/5" -> "3.3"
		s = s[:i]
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return nil
	}
	return &f
}

func splitRow(t string) []string {
	t = strings.TrimPrefix(strings.TrimSpace(t), "|")
	t = strings.TrimSuffix(t, "|")
	parts := strings.Split(t, "|")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

func colIndex(header []string) map[string]int {
	m := map[string]int{}
	for i, h := range header {
		m[strings.ToLower(strings.TrimSpace(h))] = i
	}
	return m
}

func has(header []string, name string) bool {
	for _, h := range header {
		if strings.ToLower(strings.TrimSpace(h)) == name {
			return true
		}
	}
	return false
}

func cell(cells []string, cols map[string]int, name string) string {
	if i, ok := cols[name]; ok && i < len(cells) {
		return cells[i]
	}
	return ""
}

func isSeparator(cells []string) bool {
	for _, c := range cells {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		if strings.Trim(c, "-: ") != "" {
			return false
		}
	}
	return true
}

// ParseTracker parses an applications.md document (preamble tolerated) into rows.
func ParseTracker(md string) []TrackerRow {
	var rows []TrackerRow
	var cols map[string]int
	inTable := false

	sc := bufio.NewScanner(strings.NewReader(md))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		t := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(t, "|") {
			if inTable {
				break // table ended
			}
			continue
		}
		cells := splitRow(t)
		if cols == nil {
			if has(cells, "company") && has(cells, "status") && has(cells, "role") {
				cols = colIndex(cells)
				inTable = true
			}
			continue
		}
		if isSeparator(cells) {
			continue
		}
		// Score and Status sit at the same index in both layouts.
		r := TrackerRow{
			Score:  parseScore(cell(cells, cols, "score")),
			Status: normStatus(cell(cells, cols, "status")),
		}
		r.Num, _ = strconv.Atoi(cell(cells, cols, "#"))

		// The tracker mixes two layouts: standard rows carry a Date column
		// (# | Date | Company | Role | ...); legacy migrated rows omit it
		// (# | Company | Role | <state> | ...), shifting Company/Role one cell
		// left. Detect per-row by whether the date-column cell is a real date.
		dateIdx, hasDateCol := cols["date"]
		companyIdx := cols["company"]
		if d := strings.TrimSpace(cell(cells, cols, "date")); hasDateCol {
			if ts, err := time.Parse("2006-01-02", d); err == nil {
				r.Date, r.HasDate = ts, true
				r.Company = cell(cells, cols, "company")
				r.Role = cell(cells, cols, "role")
			} else {
				// legacy: no date — Company is where Date would be, Role next.
				if dateIdx < len(cells) {
					r.Company = strings.TrimSpace(cells[dateIdx])
				}
				if companyIdx < len(cells) {
					r.Role = strings.TrimSpace(cells[companyIdx])
				}
			}
		} else {
			r.Company = cell(cells, cols, "company")
			r.Role = cell(cells, cols, "role")
		}
		if r.Company == "" && r.Role == "" {
			continue
		}
		rows = append(rows, r)
	}
	return rows
}
