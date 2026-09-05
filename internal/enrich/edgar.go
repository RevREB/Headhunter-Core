package enrich

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/RevREB/Headhunter-Core/internal/engine"
)

// EDGAR marks US public companies using SEC's public company_tickers.json
// (normalized-name match). A hit means the company is publicly traded and yields
// its ticker + CIK — a strong, authoritative stage signal. Non-matches (the vast
// majority — private companies) simply contribute nothing.
type EDGAR struct{}

func (EDGAR) Name() string { return "sec_edgar" }

type edgarEntry struct {
	CIK    int    `json:"cik_str"`
	Ticker string `json:"ticker"`
	Title  string `json:"title"`
}

var (
	edgarOnce  sync.Once
	edgarByNrm map[string]edgarEntry
)

// loadEDGAR fetches and indexes the SEC ticker file once, keyed by normalized
// company name. Failure leaves an empty index (the enricher becomes a no-op).
func loadEDGAR(ctx context.Context) {
	edgarOnce.Do(func() {
		edgarByNrm = map[string]edgarEntry{}
		body, err := httpGet(ctx, "https://www.sec.gov/files/company_tickers.json", "application/json")
		if err != nil {
			return
		}
		var raw map[string]edgarEntry
		if json.Unmarshal(body, &raw) != nil {
			return
		}
		for _, e := range raw {
			if n := engine.NormalizeCompany(e.Title); n != "" {
				edgarByNrm[n] = e // first writer wins; ties are rare and benign
			}
		}
	})
}

func (EDGAR) Enrich(ctx context.Context, h *Hints) ([]Field, error) {
	loadEDGAR(ctx)
	if len(edgarByNrm) == 0 || h.Norm == "" {
		return nil, nil
	}
	e, ok := edgarByNrm[h.Norm]
	if !ok {
		return nil, nil
	}
	return []Field{
		{Key: "stage", Value: "public", Source: "sec_edgar", Detail: fmt.Sprintf("CIK %010d", e.CIK)},
		{Key: "ticker", Value: e.Ticker, Source: "sec_edgar"},
	}, nil
}
