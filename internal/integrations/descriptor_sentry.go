package integrations

import (
	"encoding/json"
	"fmt"
)

// sentryConfig stores Mode explicitly rather than inferring it from which
// fields happen to be filled — the old Lotus's SentryMode() derived
// shared/dedicated/none from field presence, which meant clearing
// service_tag_value silently widened a binding from "issues tagged
// bpm-service" to "every issue in the shared backend project" with no
// error at all. Mode is a first-class field precisely so that mistake
// can't happen here: ValidateConfig requires it outright, and requires
// ServiceTagValue specifically when Mode is "shared".
type sentryConfig struct {
	Mode            string   `json:"mode"` // "shared" | "dedicated" — required, never inferred
	ServiceTagKey   string   `json:"service_tag_key"`
	ServiceTagValue string   `json:"service_tag_value"`
	Environments    []string `json:"environments"`
	IssueQuery      string   `json:"issue_query"`
	StatsPeriod     string   `json:"stats_period"`
	MaxIssues       int      `json:"max_issues"`
}

type sentryDescriptor struct{}

func (sentryDescriptor) Kind() string             { return "sentry" }
func (sentryDescriptor) ExternalKeyLabel() string { return "Sentry project slug" }

func (sentryDescriptor) ConfigFields() []FormField {
	return []FormField{
		{Key: "mode", Label: "Mode", Type: "select", Options: []string{"shared", "dedicated"}, Required: true},
		{Key: "service_tag_key", Label: "Service tag key", Type: "text", Default: "service", RequiredIf: map[string]string{"mode": "shared"}},
		{Key: "service_tag_value", Label: "Service tag value", Type: "text", RequiredIf: map[string]string{"mode": "shared"}},
		{Key: "environments", Label: "Environments", Type: "multiselect"},
		{Key: "issue_query", Label: "Issue query", Type: "text"},
		{Key: "stats_period", Label: "Stats period", Type: "text", Default: "14d"},
		{Key: "max_issues", Label: "Max issues", Type: "number", Default: "100"},
	}
}

func (sentryDescriptor) ValidateConfig(raw json.RawMessage) (json.RawMessage, error) {
	var c sentryConfig
	if err := decodeConfig(raw, &c); err != nil {
		return nil, err
	}
	if c.Mode != "shared" && c.Mode != "dedicated" {
		return nil, fmt.Errorf(`mode must be "shared" or "dedicated", got %q`, c.Mode)
	}
	if c.Mode == "shared" {
		if c.ServiceTagKey == "" {
			c.ServiceTagKey = "service"
		}
		if c.ServiceTagValue == "" {
			return nil, fmt.Errorf("service_tag_value is required when mode is \"shared\"")
		}
	}
	if c.StatsPeriod == "" {
		c.StatsPeriod = "14d"
	}
	if c.MaxIssues == 0 {
		c.MaxIssues = 100
	}
	return json.Marshal(c)
}
