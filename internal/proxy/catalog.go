package proxy

import "sort"

// Route maps a friendly model name to the request-body "model" value and the
// X-Request-Model header value. ReqModel may be empty, in which case the header
// is omitted — some models (e.g. glm-5.2 on non-BYOK accounts) route correctly
// from the body model alone and are forced to a fallback (deepseek-v4-pro) when
// a provider-prefixed X-Request-Model they aren't entitled to is sent.
type Route struct {
	Body     string // request-body "model" value
	ReqModel string // X-Request-Model header; "" = omit the header
}

// catalog is the static, editable model table. Add a line here to expose a new
// AutoClaw model. Keys are the friendly ids clients send as "model".
//
// Values were verified live against these accounts:
//   - glm-5.2:     body "glm-5.2", no header      -> GLM-5.2 (Z.ai). The
//     openrouter_/zai_ prefixes need BYOK and otherwise fall back to deepseek.
//   - glm-5-turbo: body "glm-5-turbo", zai_ header -> glm-5-turbo.
//   - auto:        body "auto", zai_auto header    -> upstream picks (deepseek).
var catalog = map[string]Route{
	"glm-5.2":     {Body: "glm-5.2", ReqModel: ""},
	"glm-5-turbo": {Body: "glm-5-turbo", ReqModel: "zai_glm-5-turbo"},
	"auto":        {Body: "auto", ReqModel: "zai_auto"},
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
