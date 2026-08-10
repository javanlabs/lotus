package integrations

import "encoding/json"

// gitlabConfig mirrors the spec's table: fields the scheduler and health
// rollup never need to know about (task 2.3's typed-vs-JSONB rule) live
// here, not as columns on metric_bindings.
type gitlabConfig struct {
	DefaultBranch      string `json:"default_branch"`
	StaleBranchDays    int    `json:"stale_branch_days"`
	PipelineSampleSize int    `json:"pipeline_sample_size"`
	CommitWindowDays   int    `json:"commit_window_days"`
}

type gitlabDescriptor struct{}

func (gitlabDescriptor) Kind() string { return "gitlab" }
func (gitlabDescriptor) ExternalKeyLabel() string {
	return "GitLab project path (e.g. group/subgroup/project)"
}

func (gitlabDescriptor) ConfigFields() []FormField {
	return []FormField{
		{Key: "default_branch", Label: "Default branch", Type: "text", Default: "main"},
		{Key: "stale_branch_days", Label: "Stale branch threshold (days)", Type: "number", Default: "30"},
		{Key: "pipeline_sample_size", Label: "Pipeline sample size", Type: "number", Default: "20"},
		{Key: "commit_window_days", Label: "Commit window (days)", Type: "number", Default: "30"},
	}
}

func (gitlabDescriptor) ValidateConfig(raw json.RawMessage) (json.RawMessage, error) {
	var c gitlabConfig
	if err := decodeConfig(raw, &c); err != nil {
		return nil, err
	}
	if c.DefaultBranch == "" {
		c.DefaultBranch = "main"
	}
	if c.StaleBranchDays == 0 {
		c.StaleBranchDays = 30
	}
	if c.PipelineSampleSize == 0 {
		c.PipelineSampleSize = 20
	}
	if c.CommitWindowDays == 0 {
		c.CommitWindowDays = 30
	}
	return json.Marshal(c)
}
