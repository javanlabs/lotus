package web

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/repo/global"

	// Registers each collector's Descriptor() — settings_connections.go's
	// new-connection form reads ConnectionFields from the process-wide
	// registry (task 3.1), same precedent as bindview_test.go.
	_ "github.com/uyab/lotus/internal/collector/gitlab"
	_ "github.com/uyab/lotus/internal/collector/sentry"
	_ "github.com/uyab/lotus/internal/collector/sonarqube"
	_ "github.com/uyab/lotus/internal/collector/trivy"
)

// seedSecondMember adds another user as a member of an *existing* org —
// unlike seedMember (projects_test.go), which always creates a fresh org.
// Several task 4.5 flows (role changes, ownership transfer) are inherently
// about a second member already in the same organization.
func seedSecondMember(t *testing.T, db *sqlx.DB, orgID uuid.UUID, emailPrefix, roleKey string) uuid.UUID {
	t.Helper()
	var userID uuid.UUID
	if err := db.Get(&userID, `INSERT INTO users (email, name, email_verified_at) VALUES ($1, $1, now()) RETURNING id`, emailPrefix+"@example.com"); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	var roleID uuid.UUID
	if err := db.Get(&roleID, `SELECT id FROM roles WHERE key = $1 AND org_id IS NULL`, roleKey); err != nil {
		t.Fatalf("lookup role %s: %v", roleKey, err)
	}
	if _, err := db.Exec(`INSERT INTO memberships (org_id, user_id, role_id) VALUES ($1, $2, $3)`, orgID, userID, roleID); err != nil {
		t.Fatalf("insert membership: %v", err)
	}
	return userID
}

func postForm(t *testing.T, app interface {
	Test(req *http.Request, msTimeout ...int) (*http.Response, error)
}, host, path, raw string, form url.Values) *http.Response {
	t.Helper()
	req := reqWithSession(http.MethodPost, host, path, raw)
	req.Body = io.NopCloser(strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.ContentLength = int64(len(form.Encode()))
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	return resp
}

func TestSettingsConnections_MemberCanReadButCannotWrite(t *testing.T) {
	app, db := newDashboardTestApp(t)
	orgID, ownerID := seedMember(t, db, "acme-conn-perm", "owner")
	memberID := seedSecondMember(t, db, orgID, "acme-conn-perm-member", "member")
	ownerRaw := seedSession(t, db, orgID, ownerID)
	memberRaw := seedSession(t, db, orgID, memberID)
	host := "acme-conn-perm." + dashboardTestDomain

	createForm := url.Values{"kind": {"gitlab"}, "name": {"gitlab.com"}, "base_url": {"https://gitlab.com"}, "token": {"glpat-abc123"}}
	createResp := postForm(t, app, host, "/settings/connections", ownerRaw, createForm)
	createResp.Body.Close()
	if createResp.StatusCode != http.StatusFound {
		t.Fatalf("owner create status = %d, want 302", createResp.StatusCode)
	}

	listResp, err := app.Test(reqWithSession(http.MethodGet, host, "/settings/connections", memberRaw))
	if err != nil {
		t.Fatalf("member list request: %v", err)
	}
	defer listResp.Body.Close()
	if listResp.StatusCode != http.StatusOK {
		t.Fatalf("member list status = %d, want 200 (connection.read is granted to Member)", listResp.StatusCode)
	}
	body, _ := io.ReadAll(listResp.Body)
	if !strings.Contains(string(body), "gitlab.com") {
		t.Fatalf("member's connection list missing the connection name:\n%s", body)
	}
	if strings.Contains(string(body), "glpat-abc123") {
		t.Fatalf("member's connection list leaked the raw secret:\n%s", body)
	}

	memberWriteResp := postForm(t, app, host, "/settings/connections", memberRaw,
		url.Values{"kind": {"gitlab"}, "name": {"evil"}, "base_url": {"https://gitlab.com"}, "token": {"x"}})
	memberWriteResp.Body.Close()
	if memberWriteResp.StatusCode != http.StatusForbidden {
		t.Fatalf("member create status = %d, want 403 (connection.secret.write is Admin+ only)", memberWriteResp.StatusCode)
	}
}

func TestDeleteConnection_ShowsAffectedBindingCountAndCascadesWithAudit(t *testing.T) {
	app, db := newDashboardTestApp(t)
	orgID, userID := seedMember(t, db, "acme-conn-del", "owner")
	raw := seedSession(t, db, orgID, userID)
	host := "acme-conn-del." + dashboardTestDomain

	createForm := url.Values{"kind": {"sentry"}, "name": {"sentry.io"}, "base_url": {"https://sentry.io"}, "token": {"tok"}, "org": {"acme"}}
	createResp := postForm(t, app, host, "/settings/connections", raw, createForm)
	createResp.Body.Close()
	if createResp.StatusCode != http.StatusFound {
		t.Fatalf("create status = %d, want 302", createResp.StatusCode)
	}

	var connID, componentID, bindingID string
	withOrgTx(t, db, orgID, func(tx *sqlx.Tx) {
		if err := tx.Get(&connID, `SELECT id FROM integration_connections WHERE name = 'sentry.io'`); err != nil {
			t.Fatalf("find connection: %v", err)
		}
		var projectID string
		if err := tx.Get(&projectID, `INSERT INTO projects (org_id, slug, name) VALUES ($1,'p','Payments') RETURNING id`, orgID); err != nil {
			t.Fatalf("insert project: %v", err)
		}
		if err := tx.Get(&componentID, `INSERT INTO components (org_id, project_id, slug, name, kind) VALUES ($1,$2,'c','billing-api','backend') RETURNING id`, orgID, projectID); err != nil {
			t.Fatalf("insert component: %v", err)
		}
		if err := tx.Get(&bindingID, `INSERT INTO metric_bindings (org_id, component_id, connection_id, kind, external_key, config) VALUES ($1,$2,$3,'sentry','billing-api','{}') RETURNING id`, orgID, componentID, connID); err != nil {
			t.Fatalf("insert binding: %v", err)
		}
	})

	listResp, err := app.Test(reqWithSession(http.MethodGet, host, "/settings/connections", raw))
	if err != nil {
		t.Fatalf("list request: %v", err)
	}
	defer listResp.Body.Close()
	listBody, _ := io.ReadAll(listResp.Body)
	if !strings.Contains(string(listBody), "1 binding") {
		t.Fatalf("connections list must show the affected-binding count before delete:\n%s", listBody)
	}

	deleteResp := postForm(t, app, host, "/settings/connections/"+connID+"/delete", raw, url.Values{})
	deleteResp.Body.Close()
	if deleteResp.StatusCode != http.StatusFound {
		t.Fatalf("delete status = %d, want 302", deleteResp.StatusCode)
	}

	withOrgTx(t, db, orgID, func(tx *sqlx.Tx) {
		var connDeletedAt *string
		if err := tx.Get(&connDeletedAt, `SELECT deleted_at::text FROM integration_connections WHERE id = $1`, connID); err != nil {
			t.Fatalf("read connection deleted_at: %v", err)
		}
		if connDeletedAt == nil {
			t.Fatal("connection deleted_at is still null after delete")
		}
		var bindingDeletedAt *string
		var bindingEnabled bool
		if err := tx.QueryRow(`SELECT deleted_at::text, enabled FROM metric_bindings WHERE id = $1`, bindingID).Scan(&bindingDeletedAt, &bindingEnabled); err != nil {
			t.Fatalf("read binding: %v", err)
		}
		if bindingDeletedAt == nil {
			t.Fatal("binding deleted_at is still null — deleting the connection must cascade to its bindings")
		}
		if bindingEnabled {
			t.Fatal("binding still enabled after its connection was deleted")
		}

		var eventCount int
		var metadataJSON []byte
		if err := tx.QueryRow(`SELECT count(*) FROM audit_events WHERE action = 'connection.deleted' AND target_id = $1`, connID).Scan(&eventCount); err != nil {
			t.Fatalf("count audit events: %v", err)
		}
		if eventCount != 1 {
			t.Fatalf("audit_events rows for connection.deleted = %d, want 1", eventCount)
		}
		if err := tx.QueryRow(`SELECT metadata FROM audit_events WHERE action = 'connection.deleted' AND target_id = $1`, connID).Scan(&metadataJSON); err != nil {
			t.Fatalf("read audit metadata: %v", err)
		}
		if strings.Contains(strings.ToLower(string(metadataJSON)), "tok") {
			t.Fatalf("audit metadata must never carry the secret: %s", metadataJSON)
		}
		var meta struct {
			BindingsDeleted int `json:"bindings_deleted"`
		}
		if err := json.Unmarshal(metadataJSON, &meta); err != nil {
			t.Fatalf("unmarshal audit metadata: %v", err)
		}
		if meta.BindingsDeleted != 1 {
			t.Fatalf("audit metadata bindings_deleted = %d, want 1", meta.BindingsDeleted)
		}
	})
}

func TestSettingsMembers_InviteResendRevokeAndRoleChange(t *testing.T) {
	app, db := newDashboardTestApp(t)
	orgID, ownerID := seedMember(t, db, "acme-members", "owner")
	adminID := seedSecondMember(t, db, orgID, "acme-members-admin", "admin")
	ownerRaw := seedSession(t, db, orgID, ownerID)
	adminRaw := seedSession(t, db, orgID, adminID)
	host := "acme-members." + dashboardTestDomain

	inviteResp := postForm(t, app, host, "/settings/members/invite", ownerRaw, url.Values{"email": {"newperson@example.com"}, "role": {"member"}})
	inviteResp.Body.Close()
	if inviteResp.StatusCode != http.StatusFound {
		t.Fatalf("invite status = %d, want 302", inviteResp.StatusCode)
	}

	listResp, err := app.Test(reqWithSession(http.MethodGet, host, "/settings/members", ownerRaw))
	if err != nil {
		t.Fatalf("list request: %v", err)
	}
	body, _ := io.ReadAll(listResp.Body)
	listResp.Body.Close()
	if !strings.Contains(string(body), "newperson@example.com") {
		t.Fatalf("pending invite missing from members page:\n%s", body)
	}

	var invID string
	withOrgTx(t, db, orgID, func(tx *sqlx.Tx) {
		if err := tx.Get(&invID, `SELECT id FROM invitations WHERE email = 'newperson@example.com'`); err != nil {
			t.Fatalf("find invitation: %v", err)
		}
	})

	resendResp := postForm(t, app, host, "/settings/members/invitations/"+invID+"/resend", ownerRaw, url.Values{})
	resendResp.Body.Close()
	if resendResp.StatusCode != http.StatusFound {
		t.Fatalf("resend status = %d, want 302", resendResp.StatusCode)
	}

	// Admin cannot touch the Owner's role — authz.CanChangeRole's guard,
	// enforced in the handler, not just the coarser permission middleware.
	roleResp := postForm(t, app, host, "/settings/members/"+ownerID.String()+"/role", adminRaw, url.Values{"role": {"member"}})
	roleResp.Body.Close()
	if roleResp.StatusCode != http.StatusForbidden {
		t.Fatalf("admin demoting owner status = %d, want 403", roleResp.StatusCode)
	}

	// Admin *can* promote another admin... but not to Owner.
	promoteResp := postForm(t, app, host, "/settings/members/"+adminID.String()+"/role", adminRaw, url.Values{"role": {"owner"}})
	promoteResp.Body.Close()
	if promoteResp.StatusCode != http.StatusForbidden {
		t.Fatalf("admin self-promoting to owner status = %d, want 403", promoteResp.StatusCode)
	}

	memberID := seedSecondMember(t, db, orgID, "acme-members-target", "member")
	changeResp := postForm(t, app, host, "/settings/members/"+memberID.String()+"/role", ownerRaw, url.Values{"role": {"admin"}})
	changeResp.Body.Close()
	if changeResp.StatusCode != http.StatusFound {
		t.Fatalf("owner role change status = %d, want 302", changeResp.StatusCode)
	}
	m, err := global.ActiveMembership(t.Context(), db, memberID, orgID)
	if err != nil {
		t.Fatalf("read membership: %v", err)
	}
	if m.RoleKey != "admin" {
		t.Fatalf("role = %q, want admin", m.RoleKey)
	}

	revokeResp := postForm(t, app, host, "/settings/members/invitations/"+invID+"/revoke", ownerRaw, url.Values{})
	revokeResp.Body.Close()
	if revokeResp.StatusCode != http.StatusFound {
		t.Fatalf("revoke status = %d, want 302", revokeResp.StatusCode)
	}
	afterRevoke, err := app.Test(reqWithSession(http.MethodGet, host, "/settings/members", ownerRaw))
	if err != nil {
		t.Fatalf("list request: %v", err)
	}
	defer afterRevoke.Body.Close()
	afterBody, _ := io.ReadAll(afterRevoke.Body)
	if strings.Contains(string(afterBody), "newperson@example.com") {
		t.Fatalf("revoked invitation still shown:\n%s", afterBody)
	}
}

func TestSettingsOrganization_ProfileUpdateSubdomainChangeAndTransfer(t *testing.T) {
	app, db := newDashboardTestApp(t)
	orgID, ownerID := seedMember(t, db, "acme-org-settings", "owner")
	newOwnerID := seedSecondMember(t, db, orgID, "acme-org-newowner", "member")
	raw := seedSession(t, db, orgID, ownerID)
	host := "acme-org-settings." + dashboardTestDomain

	updateResp := postForm(t, app, host, "/settings/organization", raw, url.Values{
		"name": {"Acme Corp"}, "timezone": {"Asia/Jakarta"}, "default_sync_interval_minutes": {"30"}, "retention_days": {"120"},
	})
	updateResp.Body.Close()
	if updateResp.StatusCode != http.StatusFound {
		t.Fatalf("profile update status = %d, want 302", updateResp.StatusCode)
	}
	profile, err := global.OrgProfileByID(t.Context(), db, orgID)
	if err != nil {
		t.Fatalf("read profile: %v", err)
	}
	if profile.Name != "Acme Corp" || profile.Timezone != "Asia/Jakarta" || profile.RetentionDays != 120 {
		t.Fatalf("profile = %+v, want updated fields", profile)
	}
	if profile.DefaultSyncIntervalSeconds == nil || *profile.DefaultSyncIntervalSeconds != 1800 {
		t.Fatalf("DefaultSyncIntervalSeconds = %v, want 1800 (30 minutes)", profile.DefaultSyncIntervalSeconds)
	}

	transferResp := postForm(t, app, host, "/settings/organization/transfer-ownership", raw, url.Values{"user_id": {newOwnerID.String()}})
	transferResp.Body.Close()
	if transferResp.StatusCode != http.StatusFound {
		t.Fatalf("transfer status = %d, want 302", transferResp.StatusCode)
	}
	newOwnerM, err := global.ActiveMembership(t.Context(), db, newOwnerID, orgID)
	if err != nil {
		t.Fatalf("read new owner membership: %v", err)
	}
	if newOwnerM.RoleKey != "owner" {
		t.Fatalf("new owner's role = %q, want owner", newOwnerM.RoleKey)
	}
	oldOwnerM, err := global.ActiveMembership(t.Context(), db, ownerID, orgID)
	if err != nil {
		t.Fatalf("read old owner membership: %v", err)
	}
	if oldOwnerM.RoleKey != "admin" {
		t.Fatalf("old owner's role = %q, want admin (demoted, not removed)", oldOwnerM.RoleKey)
	}

	var eventCount int
	withOrgTx(t, db, orgID, func(tx *sqlx.Tx) {
		if err := tx.Get(&eventCount, `SELECT count(*) FROM audit_events WHERE org_id = $1 AND action = 'org.ownership_transferred'`, orgID); err != nil {
			t.Fatalf("count audit events: %v", err)
		}
	})
	if eventCount != 1 {
		t.Fatalf("audit events for ownership transfer = %d, want 1", eventCount)
	}
}

func TestSettingsOrganization_DeleteRequiresExactSubdomainConfirmation(t *testing.T) {
	app, db := newDashboardTestApp(t)
	orgID, ownerID := seedMember(t, db, "acme-org-del", "owner")
	raw := seedSession(t, db, orgID, ownerID)
	host := "acme-org-del." + dashboardTestDomain

	wrongResp := postForm(t, app, host, "/settings/organization/delete", raw, url.Values{"confirm_subdomain": {"not-the-right-slug"}})
	wrongResp.Body.Close()
	if wrongResp.StatusCode != http.StatusSeeOther {
		t.Fatalf("wrong confirmation status = %d, want 303 (redirects back to the settings page with an inline error)", wrongResp.StatusCode)
	}
	org, err := global.OrgByID(t.Context(), db, orgID)
	if err != nil {
		t.Fatalf("read org: %v", err)
	}
	if org.DeletedAt != nil {
		t.Fatal("org was deleted despite a wrong confirmation string")
	}

	rightResp := postForm(t, app, host, "/settings/organization/delete", raw, url.Values{"confirm_subdomain": {org.Subdomain}})
	rightResp.Body.Close()
	if rightResp.StatusCode != http.StatusFound {
		t.Fatalf("correct confirmation status = %d, want 302", rightResp.StatusCode)
	}
	org2, err := global.OrgByID(t.Context(), db, orgID)
	if err != nil {
		t.Fatalf("read org: %v", err)
	}
	if org2.DeletedAt == nil {
		t.Fatal("org.deleted_at is still null after a correctly-confirmed delete")
	}

	var retired bool
	if err := db.Get(&retired, `SELECT EXISTS (SELECT 1 FROM retired_slugs WHERE slug = $1)`, org2.Subdomain); err != nil {
		t.Fatalf("check retired_slugs: %v", err)
	}
	if !retired {
		t.Fatal("deleted org's subdomain was not retired")
	}
}

func TestSettingsTokens_PATPlaintextShownOnceThenNotAgain(t *testing.T) {
	app, db := newDashboardTestApp(t)
	orgID, userID := seedMember(t, db, "acme-tokens", "owner")
	raw := seedSession(t, db, orgID, userID)
	host := "acme-tokens." + dashboardTestDomain

	createResp := postForm(t, app, host, "/settings/tokens/pat", raw, url.Values{"name": {"laptop CLI"}})
	createResp.Body.Close()
	if createResp.StatusCode != http.StatusSeeOther {
		t.Fatalf("create PAT status = %d, want 303 (POST-redirect-GET, so a page refresh can't resubmit and mint a second token)", createResp.StatusCode)
	}
	loc := createResp.Header.Get("Location")
	if !strings.Contains(loc, "/settings/tokens?created=") {
		t.Fatalf("Location = %q, want /settings/tokens?created=<flash id>", loc)
	}

	firstViewResp, err := app.Test(reqWithSession(http.MethodGet, host, loc, raw))
	if err != nil {
		t.Fatalf("follow redirect: %v", err)
	}
	defer firstViewResp.Body.Close()
	firstViewBody, _ := io.ReadAll(firstViewResp.Body)
	if !strings.Contains(string(firstViewBody), "laptop CLI") || !strings.Contains(string(firstViewBody), "won't be shown again") {
		t.Fatalf("first view of the flash URL missing the plaintext banner:\n%s", firstViewBody)
	}

	secondViewResp, err := app.Test(reqWithSession(http.MethodGet, host, loc, raw))
	if err != nil {
		t.Fatalf("second request to the same flash URL: %v", err)
	}
	defer secondViewResp.Body.Close()
	secondViewBody, _ := io.ReadAll(secondViewResp.Body)
	// The flash is single-read: even reloading the exact same URL (what a
	// browser refresh on that page would do) must not show it again.
	if strings.Contains(string(secondViewBody), "won't be shown again") {
		t.Fatalf("plaintext banner reappeared on a second visit to the same flash URL:\n%s", secondViewBody)
	}

	freshResp, err := app.Test(reqWithSession(http.MethodGet, host, "/settings/tokens", raw))
	if err != nil {
		t.Fatalf("fresh GET: %v", err)
	}
	defer freshResp.Body.Close()
	freshBody, _ := io.ReadAll(freshResp.Body)
	if strings.Contains(string(freshBody), "won't be shown again") {
		t.Fatalf("plaintext banner leaked onto a plain GET with no flash id:\n%s", freshBody)
	}
}

func TestSettingsTokens_ServiceTokenRequiresAdminOrOwner(t *testing.T) {
	app, db := newDashboardTestApp(t)
	orgID, ownerID := seedMember(t, db, "acme-svc-token", "owner")
	memberID := seedSecondMember(t, db, orgID, "acme-svc-token-member", "member")
	memberRaw := seedSession(t, db, orgID, memberID)
	host := "acme-svc-token." + dashboardTestDomain
	_ = ownerID

	resp := postForm(t, app, host, "/settings/tokens/service", memberRaw, url.Values{"name": {"gitlab-ci"}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("member creating a service token status = %d, want 403", resp.StatusCode)
	}
}

func TestSettingsIngest_ListsOnlyTrivyBindingsWithComputedURL(t *testing.T) {
	app, db := newDashboardTestApp(t)
	orgID, userID := seedMember(t, db, "acme-ingest", "owner")
	raw := seedSession(t, db, orgID, userID)
	host := "acme-ingest." + dashboardTestDomain

	var bindingID string
	withOrgTx(t, db, orgID, func(tx *sqlx.Tx) {
		var projectID, componentID, connID string
		if err := tx.Get(&projectID, `INSERT INTO projects (org_id, slug, name) VALUES ($1,'p','Payments') RETURNING id`, orgID); err != nil {
			t.Fatalf("insert project: %v", err)
		}
		if err := tx.Get(&componentID, `INSERT INTO components (org_id, project_id, slug, name, kind) VALUES ($1,$2,'c','billing-api','backend') RETURNING id`, orgID, projectID); err != nil {
			t.Fatalf("insert component: %v", err)
		}
		if err := tx.Get(&connID, `INSERT INTO integration_connections (org_id, provider, name, base_url) VALUES ($1,'harbor_trivy','harbor','https://harbor.example.com') RETURNING id`, orgID); err != nil {
			t.Fatalf("insert connection: %v", err)
		}
		if err := tx.Get(&bindingID, `INSERT INTO metric_bindings (org_id, component_id, connection_id, kind, external_key, config) VALUES ($1,$2,$3,'trivy','library/billing-api','{}') RETURNING id`, orgID, componentID, connID); err != nil {
			t.Fatalf("insert binding: %v", err)
		}
	})

	resp, err := app.Test(reqWithSession(http.MethodGet, host, "/settings/ingest", raw))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d:\n%s", resp.StatusCode, body)
	}
	wantURL := "/ingest/trivy/" + bindingID
	if !strings.Contains(string(body), wantURL) {
		t.Fatalf("ingest page missing binding URL %q:\n%s", wantURL, body)
	}
	if !strings.Contains(string(body), "billing-api") {
		t.Fatalf("ingest page missing component name:\n%s", body)
	}
}

func TestActivityPage_ShowsSyncRunsAcrossTheWholeOrg(t *testing.T) {
	app, db := newDashboardTestApp(t)
	orgID, userID := seedMember(t, db, "acme-activity", "owner")
	raw := seedSession(t, db, orgID, userID)
	host := "acme-activity." + dashboardTestDomain

	withOrgTx(t, db, orgID, func(tx *sqlx.Tx) {
		var projectID, componentID, connID, bindingID string
		if err := tx.Get(&projectID, `INSERT INTO projects (org_id, slug, name) VALUES ($1,'p','Payments') RETURNING id`, orgID); err != nil {
			t.Fatalf("insert project: %v", err)
		}
		if err := tx.Get(&componentID, `INSERT INTO components (org_id, project_id, slug, name, kind) VALUES ($1,$2,'c','billing-api','backend') RETURNING id`, orgID, projectID); err != nil {
			t.Fatalf("insert component: %v", err)
		}
		if err := tx.Get(&connID, `INSERT INTO integration_connections (org_id, provider, name, base_url) VALUES ($1,'gitlab','gitlab.com','https://gitlab.com') RETURNING id`, orgID); err != nil {
			t.Fatalf("insert connection: %v", err)
		}
		if err := tx.Get(&bindingID, `INSERT INTO metric_bindings (org_id, component_id, connection_id, kind, external_key, config) VALUES ($1,$2,$3,'gitlab','g/b','{}') RETURNING id`, orgID, componentID, connID); err != nil {
			t.Fatalf("insert binding: %v", err)
		}
		if _, err := tx.Exec(`
			INSERT INTO sync_runs (org_id, connection_id, binding_id, kind, trigger, status, started_at, finished_at)
			VALUES ($1,$2,$3,'gitlab','manual','ok', now() - interval '5 minutes', now())`, orgID, connID, bindingID); err != nil {
			t.Fatalf("insert sync_runs: %v", err)
		}
	})

	resp, err := app.Test(reqWithSession(http.MethodGet, host, "/activity", raw))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d:\n%s", resp.StatusCode, body)
	}
	for _, want := range []string{"ok", "manual", "gitlab"} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("activity feed missing %q:\n%s", want, body)
		}
	}
}
