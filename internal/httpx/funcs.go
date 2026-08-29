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
	// selectOptions normalizes the argument shapes a dropdown's option
	// list arrives in — see the function below.
	"selectOptions": selectOptions,
	// ternary picks yes/no by cond — for a one-line tone/class choice
	// inline in an attribute value, where {{if}}...{{else}}...{{end}} is
	// too heavy a construct for the surrounding markup to read cleanly.
	"ternary": func(cond bool, yes, no string) string {
		if cond {
			return yes
		}
		return no
	},
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

// SelectOption is one entry in the lotus_select dropdown component
// (web/templates/components/select.html). Exported so a handler can build
// the list in Go when it comes from a struct slice rather than plain
// strings — templates cannot accumulate a slice inside a range.
type SelectOption struct {
	Value    string
	Label    string
	Disabled bool
}

// selectOptions normalizes a mixed argument list into []SelectOption, so a
// call site can write either the common case
//
//	(selectOptions .Data.Kinds)
//
// or the annotated one
//
//	(selectOptions (dict "Value" "owner" "Label" "Owner" "Disabled" true) ...)
//
// without the component needing to type-switch in template syntax, which
// html/template cannot do.
func selectOptions(items ...any) ([]SelectOption, error) {
	var out []SelectOption
	for _, item := range items {
		switch v := item.(type) {
		case string:
			out = append(out, SelectOption{Value: v, Label: v})
		case []string:
			for _, s := range v {
				out = append(out, SelectOption{Value: s, Label: s})
			}
		case SelectOption:
			out = append(out, v)
		case []SelectOption:
			out = append(out, v...)
		case map[string]any:
			opt, err := optionFromDict(v)
			if err != nil {
				return nil, err
			}
			out = append(out, opt)
		case []any:
			nested, err := selectOptions(v...)
			if err != nil {
				return nil, err
			}
			out = append(out, nested...)
		default:
			return nil, fmt.Errorf("selectOptions: unsupported option type %T", item)
		}
	}
	return out, nil
}

func optionFromDict(m map[string]any) (SelectOption, error) {
	value, ok := m["Value"].(string)
	if !ok {
		return SelectOption{}, fmt.Errorf("selectOptions: option is missing a string Value")
	}
	label, ok := m["Label"].(string)
	if !ok {
		// An option whose label is its value is common enough to be worth
		// not spelling out twice.
		label = value
	}
	disabled, _ := m["Disabled"].(bool)
	return SelectOption{Value: value, Label: label, Disabled: disabled}, nil
}
