package web

import (
	"testing"

	"github.com/uyab/lotus/internal/integrations"

	// Registers each collector's Descriptor() so connectionFieldsFor (which
	// reads the process-wide collector.Registry, task 3.1) has something to
	// find — mirrors internal/web/ingest_test.go's own precedent for trivy.
	_ "github.com/uyab/lotus/internal/collector/gitlab"
	_ "github.com/uyab/lotus/internal/collector/sentry"
	_ "github.com/uyab/lotus/internal/collector/sonarqube"
	_ "github.com/uyab/lotus/internal/collector/trivy"
)

func TestProviderForKind(t *testing.T) {
	t.Parallel()
	cases := map[string]string{"gitlab": "gitlab", "sonarqube": "sonarqube", "trivy": "harbor_trivy", "sentry": "sentry"}
	for kind, want := range cases {
		if got := providerForKind(kind); got != want {
			t.Errorf("providerForKind(%q) = %q, want %q", kind, got, want)
		}
	}
}

func TestIsValidKind(t *testing.T) {
	t.Parallel()
	for _, k := range bindableKinds {
		if !isValidKind(k) {
			t.Errorf("isValidKind(%q) = false, want true", k)
		}
	}
	if isValidKind("k6") {
		t.Error(`isValidKind("k6") = true, want false — k6 isn't a real collector kind`)
	}
	if isValidKind("") {
		t.Error(`isValidKind("") = true, want false`)
	}
}

func TestPlanConnectionFields_TrivyCombinesUsernameAndPasswordIntoOneSecret(t *testing.T) {
	t.Parallel()
	form := map[string]string{"base_url": "https://harbor.example.com", "username": "robot$lotus", "password": "s3cr3t"}
	baseURL, secret, config := planConnectionFields("trivy", func(k string) string { return form[k] })
	if baseURL != "https://harbor.example.com" {
		t.Errorf("baseURL = %q", baseURL)
	}
	if secret != "robot$lotus:s3cr3t" {
		t.Errorf("secret = %q, want %q (username:password)", secret, "robot$lotus:s3cr3t")
	}
	if len(config) != 0 {
		t.Errorf("config = %v, want empty (trivy has no other connection config)", config)
	}
}

func TestPlanConnectionFields_SentrySeparatesSecretFromPlainConfig(t *testing.T) {
	t.Parallel()
	form := map[string]string{"base_url": "https://sentry.io", "token": "tok_abc", "org": "alurkerja"}
	baseURL, secret, config := planConnectionFields("sentry", func(k string) string { return form[k] })
	if baseURL != "https://sentry.io" {
		t.Errorf("baseURL = %q", baseURL)
	}
	if secret != "tok_abc" {
		t.Errorf("secret = %q, want the token field's value alone", secret)
	}
	if config["org"] != "alurkerja" {
		t.Errorf("config[org] = %v, want alurkerja — a non-secret, non-base_url field belongs in Config", config["org"])
	}
}

func TestBuildBindingConfig_OmitsBlankFieldsAndTypesNumbersCorrectly(t *testing.T) {
	t.Parallel()
	fields := []integrations.FormField{
		{Key: "default_branch", Type: "text"},
		{Key: "stale_branch_days", Type: "number"},
		{Key: "commit_window_days", Type: "number"},
	}
	form := map[string]string{"default_branch": "main", "stale_branch_days": "45"} // commit_window_days left blank
	raw, err := buildBindingConfig(fields, func(k string) string { return form[k] })
	if err != nil {
		t.Fatalf("buildBindingConfig: %v", err)
	}
	got := string(raw)
	if got != `{"default_branch":"main","stale_branch_days":45}` {
		t.Fatalf("config = %s, want stale_branch_days as a JSON number and commit_window_days omitted entirely", got)
	}
}

func TestBuildBindingConfig_RejectsNonNumericNumberField(t *testing.T) {
	t.Parallel()
	fields := []integrations.FormField{{Key: "max_issues", Label: "Max issues", Type: "number"}}
	form := map[string]string{"max_issues": "lots"}
	if _, err := buildBindingConfig(fields, func(k string) string { return form[k] }); err == nil {
		t.Fatal("buildBindingConfig: want an error for a non-numeric number field, got nil")
	}
}

func TestBuildBindingConfig_MultiselectSplitsOnCommas(t *testing.T) {
	t.Parallel()
	fields := []integrations.FormField{{Key: "environments", Type: "multiselect"}}
	form := map[string]string{"environments": "production, staging,  qa"}
	raw, err := buildBindingConfig(fields, func(k string) string { return form[k] })
	if err != nil {
		t.Fatalf("buildBindingConfig: %v", err)
	}
	if string(raw) != `{"environments":["production","staging","qa"]}` {
		t.Fatalf("config = %s, want a trimmed 3-element list", raw)
	}
}

func TestConnectionFieldsFor_UsesTheRegisteredCollector(t *testing.T) {
	t.Parallel()
	fields := connectionFieldsFor("sentry")
	if len(fields) == 0 {
		t.Fatal("connectionFieldsFor(sentry) = empty — is the sentry collector blank-imported into this test binary?")
	}
	found := false
	for _, f := range fields {
		if f.Key == "org" {
			found = true
		}
	}
	if !found {
		t.Errorf("connectionFieldsFor(sentry) = %+v, want an \"org\" field", fields)
	}
}

// The New-connection modal's name field hardcoded "e.g. gitlab.com" in the
// template, so three of the four tabs suggested a GitLab host. Driving it
// from the kind means a fifth integration cannot reintroduce that by
// forgetting a template branch.
func TestConnectionNamePlaceholder_DiffersPerKindAndCoversEveryBindableKind(t *testing.T) {
	t.Parallel()
	seen := map[string]string{}
	for _, kind := range bindableKinds {
		got := connectionNamePlaceholder(kind)
		if got == "" {
			t.Errorf("kind %q has no placeholder", kind)
			continue
		}
		if other, dup := seen[got]; dup {
			t.Errorf("kinds %q and %q share the placeholder %q — switching tabs would look like nothing happened", other, kind, got)
		}
		seen[got] = kind
	}
}

func TestConnectionNamePlaceholder_UnknownKindStillGetsAnExample(t *testing.T) {
	t.Parallel()
	if got := connectionNamePlaceholder("something-new"); got == "" {
		t.Error("an unregistered kind rendered an empty placeholder attribute")
	}
}

func TestBuildConnectionForm_CarriesTheSelectedKindsPlaceholder(t *testing.T) {
	t.Parallel()
	for _, kind := range bindableKinds {
		view := buildConnectionForm(kind, "", nil, "")
		if view.NamePlaceholder != connectionNamePlaceholder(kind) {
			t.Errorf("buildConnectionForm(%q).NamePlaceholder = %q, want %q",
				kind, view.NamePlaceholder, connectionNamePlaceholder(kind))
		}
	}
}
