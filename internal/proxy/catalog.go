package proxy

import "sort"

// Route maps a friendly model name to the upstream provider prefix and the bare
// model id sent in the request body.
type Route struct {
	Prefix string
	Bare   string
}

// Prefixed returns the value for the X-Request-Model header, e.g. "openrouter_glm-5.2".
func (r Route) Prefixed() string { return r.Prefix + "_" + r.Bare }

// catalog is the static, editable model table. Add a line here to expose a new
// AutoClaw model. Keys are the friendly ids clients send as "model".
var catalog = map[string]Route{
	"glm-5.2":     {Prefix: "openrouter", Bare: "glm-5.2"},
	"glm-5-turbo": {Prefix: "zai", Bare: "glm-5-turbo"},
	"auto":        {Prefix: "zai", Bare: "auto"},
}

// Lookup resolves a friendly model name to its Route.
func Lookup(model string) (Route, bool) {
	r, ok := catalog[model]
	return r, ok
}

// Models returns the sorted list of friendly model ids for GET /v1/models.
func Models() []string {
	out := make([]string, 0, len(catalog))
	for k := range catalog {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
