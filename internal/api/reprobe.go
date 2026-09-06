package api

// Listing-expiry re-probe loop (#4, days-on-board). Opt-in: when reprobe_enabled
// is set, a gentle rolling loop HTTP-probes tracked posting URLs and, after K
// consecutive definitive 404/410s, marks the listing gone (disappeared_at) and
// expires passive roles. Self-contained in Core — no scraper changes needed.
// Limitation: catches removals that 404/410, not ATS pages that return 200 with
// a "no longer available" body; the scraper-v1.1 by-ATS set-diff will cover that.

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/RevREB/Headhunter-Core/internal/store"
)

func (s *Server) reprobeEnabled(ctx context.Context) bool {
	if s.Store == nil {
		return false
	}
	cfg, err := s.Store.GetAllConfig(ctx)
	if err != nil {
		return false
	}
	if raw, ok := cfg["reprobe_enabled"]; ok {
		var b bool
		if json.Unmarshal(raw, &b) == nil {
			return b
		}
	}
	return false
}

// RunReprober re-probes a small batch of posting URLs each tick when enabled.
// batch/tick are deliberately gentle so this never looks like a scrape.
func (s *Server) RunReprober(ctx context.Context) {
	if s.Store == nil {
		return
	}
	const (
		batch     = 25
		threshold = 2
		tick      = 60 * time.Second
	)
	ua := os.Getenv("ENRICH_UA")
	if ua == "" {
		ua = "Mozilla/5.0 (compatible; HeadhunterBot/1.0; +https://github.com/RevREB/Headhunter-Core)"
	}
	client := &http.Client{Timeout: 12 * time.Second}
	log.Printf("reprober: listing-expiry loop started (opt-in via reprobe_enabled)")
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if !s.reprobeEnabled(ctx) {
			continue
		}
		targets, err := s.Store.NextToProbe(ctx, batch)
		if err != nil {
			log.Printf("reprober: next-to-probe: %v", err)
			continue
		}
		gone := 0
		for _, tg := range targets {
			res := probeURL(ctx, client, ua, tg.URL)
			expired, err := s.Store.RecordProbe(ctx, tg.ID, res, threshold)
			if err != nil {
				log.Printf("reprober: record %d: %v", tg.ID, err)
				continue
			}
			if expired {
				gone++
			}
		}
		if gone > 0 {
			log.Printf("reprober: %d listing(s) marked gone/expired this pass", gone)
		}
	}
}

// probeURL classifies a posting URL. Definitive 404/410 => Gone; 2xx/3xx =>
// Alive; anything else (403/429/5xx/timeout/DNS) => Ambiguous — so bot-detection
// or transient errors never falsely expire a live listing. HEAD first, GET on
// 405/501/error (some ATS reject HEAD).
func probeURL(ctx context.Context, client *http.Client, ua, url string) store.ProbeResult {
	code := probeStatus(ctx, client, ua, http.MethodHead, url)
	if code == 0 || code == 405 || code == 501 {
		if g := probeStatus(ctx, client, ua, http.MethodGet, url); g != 0 {
			code = g
		}
	}
	switch {
	case code == 404 || code == 410:
		return store.ProbeGone
	case code >= 200 && code < 400:
		return store.ProbeAlive
	default:
		return store.ProbeAmbiguous
	}
}

func probeStatus(ctx context.Context, client *http.Client, ua, method, url string) int {
	rctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, method, url, nil)
	if err != nil {
		return 0
	}
	req.Header.Set("User-Agent", ua)
	resp, err := client.Do(req)
	if err != nil {
		return 0
	}
	resp.Body.Close()
	return resp.StatusCode
}
