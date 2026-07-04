package api

import (
	"regexp"
	"testing"
)

func TestSign_KnownVectors(t *testing.T) {
	// (timestamp, signature) pairs captured live from the AutoClaw client.
	cases := map[int64]string{
		1783106439: "6ad4a6d95693eb12f5b78c67e4c0e149",
		1783106518: "e1d99088925c0f392c47ae60d835442b",
		1783106519: "127def37274d70f77f82753b340acdf6",
		1783106751: "30a44c28c72d1d32486e68f9f103d487",
	}
	for ts, want := range cases {
		if got := sign(ts); got != want {
			t.Errorf("sign(%d) = %s, want %s", ts, got, want)
		}
	}
}

func TestSignHeaders(t *testing.T) {
	h := signHeaders(1783106518)
	if h.Get("x-auth-appid") != "100003" {
		t.Errorf("appid = %q", h.Get("x-auth-appid"))
	}
	if h.Get("x-auth-timestamp") != "1783106518" {
		t.Errorf("timestamp = %q", h.Get("x-auth-timestamp"))
	}
	if h.Get("x-auth-sign") != "e1d99088925c0f392c47ae60d835442b" {
		t.Errorf("sign = %q", h.Get("x-auth-sign"))
	}
	if h.Get("x-product") != "autoclaw" || h.Get("x-version") != "1.10.3" {
		t.Errorf("static headers wrong: %v", h)
	}
	uuidRe := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	if !uuidRe.MatchString(h.Get("x-trace-id")) {
		t.Errorf("x-trace-id not a uuid v4: %q", h.Get("x-trace-id"))
	}
}
