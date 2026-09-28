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
// bindableKinds is the canonical order the four integrations appear in
// everywhere they are listed — table columns, KPI cards, this wizard. See
// AGENTS.md: the order is a product decision, not alphabetical and not
// build order, and every other list derives from this one so the columns
// cannot drift apart screen by screen.
var bindableKinds = []string{"sonarqube", "trivy", "sentry", "gitlab"}

var kindLabels = map[string]string{
	"gitlab": "GitLab", "sonarqube": "SonarQube", "trivy": "Trivy (Harbor)", "sentry": "Sentry",
}

// connectionNamePlaceholders are the example connection names shown in the
// New-connection modal. They live here, beside kindLabels, for the reason
// AGENTS.md gives for bindableKinds: anything that varies per kind belongs
// with the kinds, not scattered through templates. The modal's placeholder
// used to be the literal "e.g. gitlab.com" regardless of the tab, which
// read as a bug on three of the four tabs.
//
// A connection is named after the server it points at, so each example is
// a plausible host for that integration.
var connectionNamePlaceholders = map[string]string{
	"sonarqube": "e.g. sonarqube.internal",
	"trivy":     "e.g. harbor.internal",
	"sentry":    "e.g. sentry.io",
	"gitlab":    "e.g. gitlab.com",
}

// connectionNamePlaceholder falls back to a generic example rather than an
// empty attribute, so an integration added without one still renders a
// sensible field.
func connectionNamePlaceholder(kind string) string {
	if p, ok := connectionNamePlaceholders[kind]; ok {
		return p
	}
	return "e.g. example.com"
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
