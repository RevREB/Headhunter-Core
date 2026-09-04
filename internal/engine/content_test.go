package engine

import (
	"encoding/json"
	"testing"
)

func TestExtractDescription(t *testing.T) {
	cases := []struct{ raw, want string }{
		{`{"content":"<p>Build <b>systems</b></p>","title":"x"}`, "Build systems"},
		{`{"descriptionPlain":"Own the platform.","note":"hi"}`, "Own the platform."},
		{`{"content":"&lt;p&gt;Run &lt;b&gt;SRE&lt;/b&gt;&lt;/p&gt;"}`, "Run SRE"}, // entity-encoded (greenhouse)
		{`{"title":"Engineer","location":"Remote"}`, ""},
		{`"just a string"`, ""},
		{``, ""},
	}
	for _, c := range cases {
		if got := ExtractDescription(json.RawMessage(c.raw)); got != c.want {
			t.Errorf("ExtractDescription(%s) = %q, want %q", c.raw, got, c.want)
		}
	}
}

// The near-dup rule: same dedup_key + ContentFingerprint within nearDupMaxBits
// (12). Multi-location listings of one role collapse; different roles sharing a
// title do not.
func TestContentFingerprintNearDup(t *testing.T) {
	jd := "Own the customer-facing platform architecture. Design scalable data infrastructure on Kubernetes, drive terraform automation, partner with sales engineering, lead technical workshops, ten plus years of infrastructure and cloud experience, mentor a team of engineers, incident response and observability at scale."
	mk := func(loc string) json.RawMessage {
		b, _ := json.Marshal(map[string]string{"content": jd + loc, "id": loc})
		return b
	}
	austin := ContentFingerprint("Solutions Architect", mk(" Based in Austin, Texas."))
	nyc := ContentFingerprint("Solutions Architect", mk(" Based in New York, New York."))
	if d := Hamming(austin, nyc); d > 12 {
		t.Errorf("multi-location near-dup Hamming=%d, want <=12 (should merge)", d)
	}
	other := ContentFingerprint("Solutions Architect", json.RawMessage(`{"content":"Customer pre-sales specialist. Fifty percent travel, product demos, RFP responses, quota carrying, no hands-on engineering, manage partner relationships and channel programs and vendor negotiations."}`))
	if d := Hamming(austin, other); d <= 12 {
		t.Errorf("different-role same-title Hamming=%d, want >12 (should stay separate)", d)
	}
}
