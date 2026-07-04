package proxy

import "testing"

func TestCatalogLookup(t *testing.T) {
	// glm-5.2 routes from the body model alone; no X-Request-Model header.
	r, ok := Lookup("glm-5.2")
	if !ok || r.Body != "glm-5.2" || r.ReqModel != "" {
		t.Fatalf("glm-5.2 = %+v ok=%v", r, ok)
	}
	// glm-5-turbo carries a zai_ header.
	rt, _ := Lookup("glm-5-turbo")
	if rt.Body != "glm-5-turbo" || rt.ReqModel != "zai_glm-5-turbo" {
		t.Fatalf("glm-5-turbo = %+v", rt)
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
