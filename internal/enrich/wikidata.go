package enrich

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
)

// Wikidata fills founded year / employee count / website for companies (useful
// for firms not on Built In). To avoid mis-hits it verifies the candidate's
// official website (P856) against the known domain when one is available; with
// no domain it accepts only a top hit that has an official website, and takes
// only low-risk numeric/date fields.
type Wikidata struct{}

func (Wikidata) Name() string { return "wikidata" }

func (Wikidata) Enrich(ctx context.Context, h *Hints) ([]Field, error) {
	if h.Name == "" {
		return nil, nil
	}
	ids, err := wdSearch(ctx, h.Name)
	if err != nil || len(ids) == 0 {
		return nil, err
	}
	ents, err := wdGetEntities(ctx, ids)
	if err != nil {
		return nil, err
	}
	var chosen *wdEntity
	for _, id := range ids {
		e, ok := ents[id]
		if !ok {
			continue
		}
		site := e.website()
		switch {
		case h.Domain != "":
			if site != "" && sameHost(site, h.Domain) {
				chosen = &e
			}
		default:
			if site != "" {
				chosen = &e
			}
		}
		if chosen != nil {
			break
		}
	}
	if chosen == nil {
		return nil, nil
	}
	var out []Field
	if y := chosen.foundedYear(); y != 0 {
		out = append(out, Field{Key: "founded", Value: y, Source: "wikidata"})
	}
	if n := chosen.employees(); n != 0 {
		out = append(out, Field{Key: "employees", Value: n, Source: "wikidata"})
	}
	if site := chosen.website(); site != "" {
		out = append(out, Field{Key: "website", Value: site, Source: "wikidata"})
	}
	return out, nil
}

// ---- Wikidata REST ----

func wdSearch(ctx context.Context, name string) ([]string, error) {
	u := "https://www.wikidata.org/w/api.php?action=wbsearchentities&format=json&language=en&type=item&limit=5&search=" +
		url.QueryEscape(name)
	body, err := httpGet(ctx, u, "application/json")
	if err != nil {
		return nil, err
	}
	var r struct {
		Search []struct {
			ID string `json:"id"`
		} `json:"search"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, err
	}
	var ids []string
	for _, s := range r.Search {
		ids = append(ids, s.ID)
	}
	return ids, nil
}

func wdGetEntities(ctx context.Context, ids []string) (map[string]wdEntity, error) {
	u := "https://www.wikidata.org/w/api.php?action=wbgetentities&format=json&props=claims&ids=" +
		url.QueryEscape(strings.Join(ids, "|"))
	body, err := httpGet(ctx, u, "application/json")
	if err != nil {
		return nil, err
	}
	var r struct {
		Entities map[string]wdEntity `json:"entities"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, err
	}
	return r.Entities, nil
}

type wdSnak struct {
	Mainsnak struct {
		Datavalue struct {
			Value json.RawMessage `json:"value"`
		} `json:"datavalue"`
	} `json:"mainsnak"`
}

type wdEntity struct {
	Claims map[string][]wdSnak `json:"claims"`
}

func (e wdEntity) firstValue(prop string) json.RawMessage {
	if snaks, ok := e.Claims[prop]; ok && len(snaks) > 0 {
		return snaks[0].Mainsnak.Datavalue.Value
	}
	return nil
}

// website returns the bare host of the official website (P856).
func (e wdEntity) website() string {
	v := e.firstValue("P856")
	if len(v) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(v, &s) != nil {
		return ""
	}
	return s
}

// foundedYear parses inception (P571), whose value is {"time":"+YYYY-..."}.
func (e wdEntity) foundedYear() int {
	v := e.firstValue("P571")
	if len(v) == 0 {
		return 0
	}
	var t struct {
		Time string `json:"time"`
	}
	if json.Unmarshal(v, &t) != nil || len(t.Time) < 5 {
		return 0
	}
	y, err := strconv.Atoi(strings.TrimPrefix(t.Time[:5], "+"))
	if err != nil {
		return 0
	}
	return y
}

// employees parses P1128, whose value is {"amount":"+164000",...}.
func (e wdEntity) employees() int {
	v := e.firstValue("P1128")
	if len(v) == 0 {
		return 0
	}
	var q struct {
		Amount string `json:"amount"`
	}
	if json.Unmarshal(v, &q) != nil {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimPrefix(q.Amount, "+"))
	if err != nil {
		return 0
	}
	return n
}

// sameHost reports whether a URL's host matches a bare domain (suffix-aware).
func sameHost(rawURL, domain string) bool {
	h := hostOf(rawURL)
	if h == "" || domain == "" {
		return false
	}
	return h == domain || strings.HasSuffix(h, "."+domain) || strings.HasSuffix(domain, "."+h)
}
