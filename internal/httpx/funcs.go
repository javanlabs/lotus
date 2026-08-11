package httpx

import (
	"fmt"
	"html/template"
)

// funcMap is shared by both rendering paths (parse and RenderPartial) so a
// partial can use the same helpers a full page can.
var funcMap = template.FuncMap{
	// dict builds a map from an alternating key/value argument list, for
	// passing multiple values into a {{template "name" (dict ...)}} call —
	// html/template has no literal map syntax of its own.
	"dict": dict,
}

func dict(kv ...any) (map[string]any, error) {
	if len(kv)%2 != 0 {
		return nil, fmt.Errorf("dict: odd number of arguments")
	}
	m := make(map[string]any, len(kv)/2)
	for i := 0; i < len(kv); i += 2 {
		key, ok := kv[i].(string)
		if !ok {
			return nil, fmt.Errorf("dict: key %v is not a string", kv[i])
		}
		m[key] = kv[i+1]
	}
	return m, nil
}
