package integrations_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/uyab/lotus/internal/integrations"
)

func TestSentryDescriptor_SharedModeRequiresServiceTagValue(t *testing.T) {
	d, err := integrations.DescriptorFor("sentry")
	if err != nil {
		t.Fatalf("DescriptorFor: %v", err)
	}
	_, err = d.ValidateConfig(json.RawMessage(`{"mode":"shared"}`))
	if err == nil {
		t.Fatal("expected mode=shared without service_tag_value to be rejected")
	}
	if !strings.Contains(err.Error(), "service_tag_value") {
		t.Fatalf("error = %q, want it to mention service_tag_value", err)
	}
}

func TestSentryDescriptor_DedicatedModeDoesNotNeedServiceTagValue(t *testing.T) {
	d, _ := integrations.DescriptorFor("sentry")
	validated, err := d.ValidateConfig(json.RawMessage(`{"mode":"dedicated"}`))
	if err != nil {
		t.Fatalf("expected dedicated mode without service_tag_value to be valid: %v", err)
	}
	var out map[string]any
	json.Unmarshal(validated, &out)
	if out["mode"] != "dedicated" {
		t.Fatalf("mode = %v, want dedicated", out["mode"])
	}
}

func TestSentryDescriptor_ModeIsRequiredNeverInferred(t *testing.T) {
	d, _ := integrations.DescriptorFor("sentry")
	if _, err := d.ValidateConfig(json.RawMessage(`{}`)); err == nil {
		t.Fatal("expected a config with no mode at all to be rejected, not defaulted")
	}
	if _, err := d.ValidateConfig(json.RawMessage(`{"mode":"nonsense"}`)); err == nil {
		t.Fatal("expected an invalid mode value to be rejected")
	}
}

func TestSentryDescriptor_SharedModeAppliesDefaultTagKey(t *testing.T) {
	d, _ := integrations.DescriptorFor("sentry")
	validated, err := d.ValidateConfig(json.RawMessage(`{"mode":"shared","service_tag_value":"bpm-service"}`))
	if err != nil {
		t.Fatalf("ValidateConfig: %v", err)
	}
	var out map[string]any
	json.Unmarshal(validated, &out)
	if out["service_tag_key"] != "service" {
		t.Fatalf("service_tag_key = %v, want default \"service\"", out["service_tag_key"])
	}
}

func TestGitLabDescriptor_AppliesDefaults(t *testing.T) {
	d, _ := integrations.DescriptorFor("gitlab")
	validated, err := d.ValidateConfig(json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("ValidateConfig: %v", err)
	}
	var out map[string]any
	json.Unmarshal(validated, &out)
	if out["default_branch"] != "main" {
		t.Fatalf("default_branch = %v, want main", out["default_branch"])
	}
	if out["stale_branch_days"] != float64(30) {
		t.Fatalf("stale_branch_days = %v, want 30", out["stale_branch_days"])
	}
}

func TestTrivyDescriptor_RejectsInvalidSource(t *testing.T) {
	d, _ := integrations.DescriptorFor("trivy")
	if _, err := d.ValidateConfig(json.RawMessage(`{"source":"not-a-real-source"}`)); err == nil {
		t.Fatal("expected an invalid source to be rejected")
	}
}

func TestDescriptor_UnknownConfigFieldRejected(t *testing.T) {
	d, _ := integrations.DescriptorFor("gitlab")
	if _, err := d.ValidateConfig(json.RawMessage(`{"defualt_branch":"main"}`)); err == nil {
		t.Fatal("expected a typo'd config field to be rejected, not silently ignored")
	}
}

func TestDescriptorFor_UnknownKindErrors(t *testing.T) {
	if _, err := integrations.DescriptorFor("not-a-real-kind"); err == nil {
		t.Fatal("expected an error for an unknown kind")
	}
}

func TestDescriptors_AllFourKindsPresent(t *testing.T) {
	all := integrations.Descriptors()
	for _, kind := range []string{"gitlab", "sonarqube", "trivy", "sentry"} {
		if _, ok := all[kind]; !ok {
			t.Errorf("Descriptors() missing kind %q", kind)
		}
	}
}
