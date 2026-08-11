package web

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/uyab/lotus/internal/collector"
	"github.com/uyab/lotus/internal/integrations"
)

// bindableKinds is the fixed set of real collector kinds the wizard offers
// — deliberately not collector.Registered() (which would silently include
// whatever happens to be blank-imported into the running binary); this is
// the app's own opinion about what a user can bind from the UI, in the
// order shown as tabs.
var bindableKinds = []string{"gitlab", "sonarqube", "trivy", "sentry"}

var kindLabels = map[string]string{
	"gitlab": "GitLab", "sonarqube": "SonarQube", "trivy": "Trivy (Harbor)", "sentry": "Sentry",
}

// providerForKind maps a binding kind to its connection provider — every
// kind is also its own provider except trivy, whose provider is
// "harbor_trivy" (see internal/integrations.BindingKindForProvider, whose
// inverse this is).
func providerForKind(kind string) string {
	if kind == "trivy" {
		return "harbor_trivy"
	}
	return kind
}

func isValidKind(kind string) bool {
	return slices.Contains(bindableKinds, kind)
}

// fieldView is what a wizard template renders one form input from —
// collector.Field (connection fields) and integrations.FormField (binding
// config fields) both flatten into this the same way, so one template
// partial renders both.
type fieldView struct {
	Key, Label, Type string
	Required         bool
	Options          []string
	Default          string
	Value            string
	RequiredIfKey    string
	RequiredIfValue  string
}

func connectionFieldViews(fields []collector.Field, values map[string]string) []fieldView {
	out := make([]fieldView, 0, len(fields))
	for _, f := range fields {
		out = append(out, fieldView{
			Key: f.Key, Label: f.Label, Type: f.Type, Required: f.Required,
			Options: f.Options, Default: f.Default, Value: values[f.Key],
		})
	}
	return out
}

func bindingFieldViews(fields []integrations.FormField, values map[string]string) []fieldView {
	out := make([]fieldView, 0, len(fields))
	for _, f := range fields {
		fv := fieldView{
			Key: f.Key, Label: f.Label, Type: f.Type, Required: f.Required,
			Options: f.Options, Default: f.Default, Value: values[f.Key],
		}
		for k, v := range f.RequiredIf {
			fv.RequiredIfKey, fv.RequiredIfValue = k, v
		}
		out = append(out, fv)
	}
	return out
}

// planConnectionFields splits a new-connection form submission into the
// three places NewConnection actually stores them: the top-level BaseURL,
// the top-level (single) Secret, and everything else as Config JSON.
//
// Trivy is a deliberate special case: Harbor needs a username *and* a
// password, but a Connection has exactly one secret blob (task 2.2), so
// internal/collector/trivy's own Open() expects Secret to already be
// "username:password" — two of its ConnectionFields (username, password)
// jointly form the one Secret rather than one field mapping to it, which
// the generic Type=="secret" rule below can't express.
func planConnectionFields(kind string, form func(key string) string) (baseURL, secret string, config map[string]any) {
	config = map[string]any{}
	if kind == "trivy" {
		return strings.TrimSpace(form("base_url")),
			strings.TrimSpace(form("username")) + ":" + strings.TrimSpace(form("password")),
			config
	}
	fields := connectionFieldsFor(kind)
	for _, f := range fields {
		val := strings.TrimSpace(form(f.Key))
		switch {
		case f.Key == "base_url":
			baseURL = val
		case f.Type == "secret":
			secret = val
		case val != "":
			config[f.Key] = val
		}
	}
	return baseURL, secret, config
}

// connectionFieldsFor returns a kind's collector.Descriptor.ConnectionFields
// — from the process-wide collector registry (task 3.1), so it only sees
// kinds actually blank-imported into this binary (cmd/lotus imports all
// four; a test binary that wants this must import them too, same as
// internal/web/ingest_test.go already does for trivy).
func connectionFieldsFor(kind string) []collector.Field {
	c, ok := collector.Get(collector.Kind(kind))
	if !ok {
		return nil
	}
	return c.Descriptor().ConnectionFields
}

// buildBindingConfig turns a submitted form into the JSON object
// repo.CreateBinding passes to the kind's Descriptor.ValidateConfig —
// which re-validates and applies defaults server-side regardless, so this
// only needs to get types roughly right (numbers as numbers, multiselect
// as a list) and omit anything left blank, not replicate that validation.
func buildBindingConfig(fields []integrations.FormField, form func(key string) string) (json.RawMessage, error) {
	m := map[string]any{}
	for _, f := range fields {
		raw := strings.TrimSpace(form(f.Key))
		if raw == "" {
			continue
		}
		switch f.Type {
		case "number":
			n, err := strconv.Atoi(raw)
			if err != nil {
				return nil, fmt.Errorf("%s must be a number", f.Label)
			}
			m[f.Key] = n
		case "multiselect":
			var parts []string
			for _, p := range strings.Split(raw, ",") {
				p = strings.TrimSpace(p)
				if p != "" {
					parts = append(parts, p)
				}
			}
			if len(parts) > 0 {
				m[f.Key] = parts
			}
		case "boolean":
			m[f.Key] = raw == "true" || raw == "on" || raw == "1"
		default:
			m[f.Key] = raw
		}
	}
	return json.Marshal(m)
}
