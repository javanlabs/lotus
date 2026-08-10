package integrations

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// FormField declares one input a metric binding's config needs — task
// 2.3's mechanism for rendering the binding form from data instead of
// hand-written per-integration HTML. Adding a fifth integration means
// writing a Descriptor, not a template.
type FormField struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Type     string `json:"type"` // "text" | "select" | "multiselect" | "number" | "boolean"
	Required bool   `json:"required"`
	// RequiredIf makes Required conditional on another field's value —
	// e.g. Sentry's service_tag_value is required only when mode=shared.
	// Empty means Required applies unconditionally.
	RequiredIf map[string]string `json:"required_if,omitempty"`
	Options    []string          `json:"options,omitempty"`
	Default    string            `json:"default,omitempty"`
}

// Descriptor is the per-kind contract task 2.3 asks for: what a binding's
// config form looks like, and how to validate a submitted config before
// it's ever written to the database. The database's own CHECK constraints
// (migration 00012) are a safety net behind this, not the other way
// around — Go is what gives a form-filler an error message, not just a
// rejected INSERT.
type Descriptor interface {
	Kind() string
	ExternalKeyLabel() string // e.g. "GitLab project path", shown next to the external_key input
	ConfigFields() []FormField
	ValidateConfig(config json.RawMessage) (json.RawMessage, error) // returns config with defaults applied
}

// Descriptors returns every kind's Descriptor, keyed by Kind().
func Descriptors() map[string]Descriptor {
	return map[string]Descriptor{
		"gitlab":    gitlabDescriptor{},
		"sonarqube": sonarqubeDescriptor{},
		"trivy":     trivyDescriptor{},
		"sentry":    sentryDescriptor{},
	}
}

func DescriptorFor(kind string) (Descriptor, error) {
	d, ok := Descriptors()[kind]
	if !ok {
		return nil, fmt.Errorf("integrations: unknown binding kind %q", kind)
	}
	return d, nil
}

// decodeConfig unmarshals raw into out, rejecting unknown fields — a typo
// in a config key (e.g. "servcie_tag_value") fails loudly here instead of
// silently being ignored and leaving the field at its zero value.
func decodeConfig(raw json.RawMessage, out any) error {
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("invalid config: %w", err)
	}
	return nil
}
