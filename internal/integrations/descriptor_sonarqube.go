package integrations

import "encoding/json"

type sonarqubeConfig struct {
	Branch     string   `json:"branch"`
	Exclusions []string `json:"exclusions"`
}

type sonarqubeDescriptor struct{}

func (sonarqubeDescriptor) Kind() string             { return "sonarqube" }
func (sonarqubeDescriptor) ExternalKeyLabel() string { return "SonarQube project key" }

func (sonarqubeDescriptor) ConfigFields() []FormField {
	return []FormField{
		{Key: "branch", Label: "Branch", Type: "text"},
		{Key: "exclusions", Label: "Path exclusions", Type: "multiselect"},
	}
}

func (sonarqubeDescriptor) ValidateConfig(raw json.RawMessage) (json.RawMessage, error) {
	var c sonarqubeConfig
	if err := decodeConfig(raw, &c); err != nil {
		return nil, err
	}
	return json.Marshal(c)
}
