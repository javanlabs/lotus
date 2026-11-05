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
	// inc turns a range index into a 1-based position ("Step 1").
	"inc": func(i int) int { return i + 1 },
	// ternary picks yes/no by cond — for a one-line tone/class choice
	// inline in an attribute value, where {{if}}...{{else}}...{{end}} is
	// too heavy a construct for the surrounding markup to read cleanly.
	// providerLabel turns a provider registry key into something worth
	// showing a human. The keys are URL segments — lowercase, no spaces —
	// so rendering them raw produced "Continue with github".
	"providerLabel": func(name string) string {
		switch name {
		case "github":
			return "GitHub"
		case "google":
			return "Google"
		case "sso":
			// Deliberately generic. Which OIDC server is behind this is
			// the operator's business, not a detail to publish on an
			// unauthenticated login page.
			return "SSO"
		default:
			return name
		}
	},
	"ternary": func(cond bool, yes, no string) string {
		if cond {
			return yes
		}
		return no
	},
	// safeCSS passes a declaration list through into a style="" attribute.
	//
	// html/template's CSS sanitizer allows a single VALUE there, not a list
	// of declarations, so `style="{{.Style}}"` with "display:flex;gap:8px"
	// renders as ZgotmplZ. Components that take a style passthrough — see
	// lotus_card — need this to exist.
	//
	// It is an escape hatch and disables escaping for what it is given, so
	// it takes ONLY literals written in a template by us. Never hand it a
	// value that came from a request, a database row or a config file.
	"safeCSS": func(s string) template.CSS { return template.CSS(s) },
	// safeAttrs passes a run of attributes ("x-data=\"{open:false}\"") into
	// a tag. Same escape hatch as safeCSS and the same rule: template
	// literals written by us only, never a value from outside.
	"safeAttrs": func(s string) template.HTMLAttr { return template.HTMLAttr(s) },
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
	// Tree options (a section picker): the list shows Name indented by
	// Depth levels, while Label — the full path — is what the box shows
	// once chosen and what typing filters on. An option with no Name is a
	// plain one and shows Label.
	Name  string
	Depth int
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
