package proxy

import "testing"

func TestCatalogLookup(t *testing.T) {
	r, ok := Lookup("glm-5.2")
	if !ok || r.Prefix != "openrouter" || r.Bare != "glm-5.2" {
		t.Fatalf("glm-5.2 = %+v ok=%v", r, ok)
	}
	if r.Prefixed() != "openrouter_glm-5.2" {
		t.Fatalf("prefixed = %s", r.Prefixed())
	}
	if _, ok := Lookup("nope"); ok {
		t.Fatal("unknown model should not resolve")
	}
}

func TestCatalogModels(t *testing.T) {
	got := Models()
	if len(got) < 3 {
		t.Fatalf("expected >=3 models, got %v", got)
	}
	found := map[string]bool{}
	for _, m := range got {
		found[m] = true
	}
	for _, want := range []string{"glm-5.2", "glm-5-turbo", "auto"} {
		if !found[want] {
			t.Fatalf("catalog missing %s: %v", want, got)
		}
	}
}
