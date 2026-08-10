package integrations

import (
	"encoding/json"
	"fmt"
	"slices"
)

type trivyConfig struct {
	Source        string `json:"source"` // "registry" | "push"
	TagPattern    string `json:"tag_pattern"`
	SeverityFloor string `json:"severity_floor"` // "low" | "medium" | "high" | "critical"
}

var trivySeverities = []string{"low", "medium", "high", "critical"}

type trivyDescriptor struct{}

func (trivyDescriptor) Kind() string             { return "trivy" }
func (trivyDescriptor) ExternalKeyLabel() string { return "Harbor repository (e.g. project/repo)" }

func (trivyDescriptor) ConfigFields() []FormField {
	return []FormField{
		{Key: "source", Label: "Source", Type: "select", Options: []string{"registry", "push"}, Default: "registry"},
		{Key: "tag_pattern", Label: "Tag pattern", Type: "text"},
		{Key: "severity_floor", Label: "Minimum severity", Type: "select", Options: trivySeverities, Default: "high"},
	}
}

func (trivyDescriptor) ValidateConfig(raw json.RawMessage) (json.RawMessage, error) {
	var c trivyConfig
	if err := decodeConfig(raw, &c); err != nil {
		return nil, err
	}
	if c.Source == "" {
		c.Source = "registry"
	}
	if c.Source != "registry" && c.Source != "push" {
		return nil, fmt.Errorf("source must be \"registry\" or \"push\", got %q", c.Source)
	}
	if c.SeverityFloor == "" {
		c.SeverityFloor = "high"
	}
	if !slices.Contains(trivySeverities, c.SeverityFloor) {
		return nil, fmt.Errorf("severity_floor must be one of %v, got %q", trivySeverities, c.SeverityFloor)
	}
	return json.Marshal(c)
}
