package web

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/repo"
	"github.com/uyab/lotus/internal/repo/global"
)

func TestStatusTone(t *testing.T) {
	cases := map[string]string{"ok": "pass", "pending": "none", "auth_failed": "fail", "error": "fail"}
	for status, want := range cases {
		if got := statusTone(status); got != want {
			t.Errorf("statusTone(%q) = %q, want %q", status, got, want)
		}
	}
}

func TestProviderLabel(t *testing.T) {
	cases := map[string]string{"gitlab": "GitLab", "sonarqube": "SonarQube", "harbor_trivy": "Trivy (Harbor)", "sentry": "Sentry", "unknown": "unknown"}
	for provider, want := range cases {
		if got := providerLabel(provider); got != want {
			t.Errorf("providerLabel(%q) = %q, want %q", provider, got, want)
		}
	}
}

func TestBuildConnectionRow_FlattensNullableFieldsAndComputesDisplayStrings(t *testing.T) {
	last4 := "abcd"
	updatedAt := time.Now().Add(-2 * time.Hour)
	verifiedAt := time.Now().Add(-1 * time.Hour)
	errMsg := "connection refused"
	conn := repo.Connection{
		ID: uuid.New(), Provider: "gitlab", Name: "gitlab.com", BaseURL: "https://gitlab.com",
		SecretLast4: &last4, SecretUpdatedAt: &updatedAt, LastVerifiedAt: &verifiedAt, LastError: &errMsg, Status: "auth_failed",
	}
	row := buildConnectionRow(conn, 3)
	if row.ProviderLabel != "GitLab" {
		t.Errorf("ProviderLabel = %q", row.ProviderLabel)
	}
	if row.StatusTone != "fail" {
		t.Errorf("StatusTone = %q, want fail for auth_failed", row.StatusTone)
	}
	if row.SecretLast4 != "•••• abcd" {
		t.Errorf("SecretLast4 = %q", row.SecretLast4)
	}
	if row.ActiveBindings != 3 {
		t.Errorf("ActiveBindings = %d, want 3", row.ActiveBindings)
	}
}

func TestBuildConnectionRow_NilSecretFieldsRenderDash(t *testing.T) {
	conn := repo.Connection{ID: uuid.New(), Provider: "sentry", Name: "sentry.io", BaseURL: "https://sentry.io", Status: "pending"}
	row := buildConnectionRow(conn, 0)
	if row.SecretLast4 != "" {
		t.Errorf("SecretLast4 = %q, want empty for a never-set secret", row.SecretLast4)
	}
	if row.SecretUpdatedAt != "–" || row.LastVerifiedAt != "–" {
		t.Errorf("SecretUpdatedAt/LastVerifiedAt = %q/%q, want dash placeholders", row.SecretUpdatedAt, row.LastVerifiedAt)
	}
}

func TestBuildTokenRow_NeverUsedNeverExpiresRendersPlaceholders(t *testing.T) {
	tok := global.APIToken{ID: uuid.New(), Name: "laptop", TokenPrefix: "lotus_pat_ab12"}
	row := buildTokenRow(tok)
	if row.LastUsedAt != "never" || row.ExpiresAt != "never" {
		t.Errorf("LastUsedAt/ExpiresAt = %q/%q, want never/never", row.LastUsedAt, row.ExpiresAt)
	}
}
