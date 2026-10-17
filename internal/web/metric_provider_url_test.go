package web

import (
	"encoding/json"
	"testing"

	"github.com/uyab/lotus/internal/repo"
)

// The metric header links a binding's key to what it reads in the
// integration's own UI.
func TestMetricChrome_ProviderURL(t *testing.T) {
	t.Parallel()
	chrome := func(kind, base, key, cfg, connCfg string) metricChrome {
		return metricChrome{
			Binding:    repo.Binding{Kind: kind, ExternalKey: key, Config: json.RawMessage(cfg)},
			Connection: repo.Connection{BaseURL: base, Config: json.RawMessage(connCfg)},
		}
	}
	cases := []struct {
		name string
		m    metricChrome
		want string
	}{
		{"sonarqube", chrome("sonarqube", "https://sonar.javan.co.id/", "alurkerja-app-react-new", `{}`, `{}`),
			"https://sonar.javan.co.id/dashboard?id=alurkerja-app-react-new"},
		{"harbor image", chrome("trivy", "https://harbor.javan.co.id", "alurkerja-v2/alurkerja-apps-release", `{}`, `{}`),
			"https://harbor.javan.co.id/harbor/projects/alurkerja-v2/repositories/alurkerja-apps-release"},
		{"repository scan", chrome("trivy", "https://gitlab.javan.co.id", "alurkerja/on-premises/camunda-bpm-platform", `{"source":"repo"}`, `{}`),
			"https://gitlab.javan.co.id/alurkerja/on-premises/camunda-bpm-platform"},
		{"gitlab", chrome("gitlab", "https://gitlab.javan.co.id", "alurkerja/app-react", `{}`, `{}`),
			"https://gitlab.javan.co.id/alurkerja/app-react"},
		{"sentry before a sync", chrome("sentry", "https://sentry.io", "alurkerja-javan-fe", `{"mode":"shared"}`, `{"org":"javan"}`),
			"https://sentry.io/organizations/javan/projects/alurkerja-javan-fe/"},
		{"no base URL", chrome("gitlab", "", "a/b", `{}`, `{}`), ""},
	}
	for _, c := range cases {
		if got := c.m.ProviderURL(); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
	// Once synced, Sentry opens the project's issue stream as the sync
	// filtered it.
	m := chrome("sentry", "https://sentry.io", "alurkerja-javan-fe", `{"mode":"shared"}`, `{"org":"javan"}`)
	m.SentryEnvs = []repo.SentryEnvironmentSummary{{SentrySnapshot: repo.SentrySnapshot{SentryProjectID: 42, StatusFilter: "is:unresolved"}}}
	if got := m.ProviderURL(); got != "https://sentry.io/organizations/javan/issues/?project=42&query=is%3Aunresolved&statsPeriod=14d" {
		t.Errorf("synced sentry: %q", got)
	}
}
