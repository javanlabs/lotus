package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/jmoiron/sqlx"
)

func TestProjectPage_ShowsComponentsViaTheSameDashboardBlock(t *testing.T) {
	app, db := newDashboardTestApp(t)
	orgID, userID := seedMember(t, db, "acme-projpage", "owner")
	raw := seedSession(t, db, orgID, userID)
	host := "acme-projpage." + dashboardTestDomain

	var projectID string
	withOrgTx(t, db, orgID, func(tx *sqlx.Tx) {
		if err := tx.Get(&projectID, `INSERT INTO projects (org_id, slug, name) VALUES ($1,'p','Payments') RETURNING id`, orgID); err != nil {
			t.Fatalf("insert project: %v", err)
		}
		if _, err := tx.Exec(`INSERT INTO components (org_id, project_id, slug, name, kind) VALUES ($1,$2,'c','billing-api','backend')`, orgID, projectID); err != nil {
			t.Fatalf("insert component: %v", err)
		}
	})

	resp, err := app.Test(reqWithSession(http.MethodGet, host, "/projects/"+projectID, raw))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d:\n%s", resp.StatusCode, body)
	}
	body, _ := io.ReadAll(resp.Body)
	for _, want := range []string{"Payments", "billing-api", "Add component"} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("body missing %q:\n%s", want, body)
		}
	}
}

func TestProjectPage_UnknownIDIs404NotAnotherOrgsProject(t *testing.T) {
	app, db := newDashboardTestApp(t)
	orgA, userA := seedMember(t, db, "acme-proj-a", "owner")
	orgB, _ := seedMember(t, db, "acme-proj-b", "owner")
	raw := seedSession(t, db, orgA, userA)

	var otherProjectID string
	withOrgTx(t, db, orgB, func(tx *sqlx.Tx) {
		if err := tx.Get(&otherProjectID, `INSERT INTO projects (org_id, slug, name) VALUES ($1,'p','Other Org Project') RETURNING id`, orgB); err != nil {
			t.Fatalf("insert project: %v", err)
		}
	})

	host := "acme-proj-a." + dashboardTestDomain
	resp, err := app.Test(reqWithSession(http.MethodGet, host, "/projects/"+otherProjectID, raw))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (RLS must hide another org's project, not show it)", resp.StatusCode)
	}
}

func TestAddComponent_CreatesAndRedirectsToComponentPage(t *testing.T) {
	app, db := newDashboardTestApp(t)
	orgID, userID := seedMember(t, db, "acme-addcomp", "owner")
	raw := seedSession(t, db, orgID, userID)
	host := "acme-addcomp." + dashboardTestDomain

	var projectID string
	withOrgTx(t, db, orgID, func(tx *sqlx.Tx) {
		if err := tx.Get(&projectID, `INSERT INTO projects (org_id, slug, name) VALUES ($1,'p','Payments') RETURNING id`, orgID); err != nil {
			t.Fatalf("insert project: %v", err)
		}
	})

	form := url.Values{"name": {"Billing API"}, "slug": {"billing-api"}, "kind": {"backend"}, "language": {"Go"}}
	req := httptest.NewRequest(http.MethodPost, "http://"+host+"/projects/"+projectID+"/components/new", strings.NewReader(form.Encode()))
	req.Host = host
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "lotus_session", Value: raw})

	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 302:\n%s", resp.StatusCode, body)
	}
	loc := resp.Header.Get("Location")
	if !strings.HasPrefix(loc, "/components/") {
		t.Fatalf("Location = %q, want /components/<id>", loc)
	}

	compResp, err := app.Test(reqWithSession(http.MethodGet, host, loc, raw))
	if err != nil {
		t.Fatalf("follow redirect: %v", err)
	}
	defer compResp.Body.Close()
	body, _ := io.ReadAll(compResp.Body)
	if !strings.Contains(string(body), "Billing API") {
		t.Fatalf("component page missing the new component's name:\n%s", body)
	}
}

func TestBindWizard_CreatesNewConnectionAndBindingTogether(t *testing.T) {
	app, db := newDashboardTestApp(t)
	orgID, userID := seedMember(t, db, "acme-bindwiz", "owner")
	raw := seedSession(t, db, orgID, userID)
	host := "acme-bindwiz." + dashboardTestDomain

	var projectID, componentID string
	withOrgTx(t, db, orgID, func(tx *sqlx.Tx) {
		if err := tx.Get(&projectID, `INSERT INTO projects (org_id, slug, name) VALUES ($1,'p','Payments') RETURNING id`, orgID); err != nil {
			t.Fatalf("insert project: %v", err)
		}
		if err := tx.Get(&componentID, `INSERT INTO components (org_id, project_id, slug, name, kind) VALUES ($1,$2,'c','billing-api','backend') RETURNING id`, orgID, projectID); err != nil {
			t.Fatalf("insert component: %v", err)
		}
	})

	// First, the GET form for gitlab renders the right external-key label
	// and connection-field inputs.
	getResp, err := app.Test(reqWithSession(http.MethodGet, host, "/components/"+componentID+"/bind?kind=gitlab", raw))
	if err != nil {
		t.Fatalf("GET bind form: %v", err)
	}
	defer getResp.Body.Close()
	getBody, _ := io.ReadAll(getResp.Body)
	for _, want := range []string{"GitLab project path", `name="token"`, `name="base_url"`} {
		if !strings.Contains(string(getBody), want) {
			t.Fatalf("bind form missing %q:\n%s", want, getBody)
		}
	}

	form := url.Values{
		"kind": {"gitlab"}, "connection_id": {"new"}, "new_connection_name": {"gitlab.com"},
		"base_url": {"https://gitlab.com"}, "token": {"glpat-faketoken"},
		"external_key": {"alurkerja/billing-api"}, "default_branch": {"main"},
	}
	req := httptest.NewRequest(http.MethodPost, "http://"+host+"/components/"+componentID+"/bind", strings.NewReader(form.Encode()))
	req.Host = host
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "lotus_session", Value: raw})

	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("POST bind: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 302:\n%s", resp.StatusCode, body)
	}

	var connCount, bindingCount int
	withOrgTx(t, db, orgID, func(tx *sqlx.Tx) {
		if err := tx.Get(&connCount, `SELECT count(*) FROM integration_connections WHERE org_id=$1 AND provider='gitlab' AND name='gitlab.com'`, orgID); err != nil {
			t.Fatalf("count connections: %v", err)
		}
		if err := tx.Get(&bindingCount, `SELECT count(*) FROM metric_bindings WHERE org_id=$1 AND component_id=$2 AND kind='gitlab' AND external_key='alurkerja/billing-api'`, orgID, componentID); err != nil {
			t.Fatalf("count bindings: %v", err)
		}
	})
	if connCount != 1 {
		t.Errorf("connections matching = %d, want 1 (the wizard should have created it)", connCount)
	}
	if bindingCount != 1 {
		t.Errorf("bindings matching = %d, want 1", bindingCount)
	}

	// The component page now shows the binding, no longer the empty state.
	compResp, err := app.Test(reqWithSession(http.MethodGet, host, "/components/"+componentID, raw))
	if err != nil {
		t.Fatalf("GET component page: %v", err)
	}
	defer compResp.Body.Close()
	compBody, _ := io.ReadAll(compResp.Body)
	for _, want := range []string{"alurkerja/billing-api", "gitlab.com"} {
		if !strings.Contains(string(compBody), want) {
			t.Fatalf("component page missing %q:\n%s", want, compBody)
		}
	}
	if strings.Contains(string(compBody), "No metrics bound yet") {
		t.Fatalf("component page still shows the empty state after a binding exists:\n%s", compBody)
	}
}

func TestBindWizard_MissingExternalKeyReRendersWithError(t *testing.T) {
	app, db := newDashboardTestApp(t)
	orgID, userID := seedMember(t, db, "acme-bindwiz-err", "owner")
	raw := seedSession(t, db, orgID, userID)
	host := "acme-bindwiz-err." + dashboardTestDomain

	var projectID, componentID string
	withOrgTx(t, db, orgID, func(tx *sqlx.Tx) {
		if err := tx.Get(&projectID, `INSERT INTO projects (org_id, slug, name) VALUES ($1,'p','Payments') RETURNING id`, orgID); err != nil {
			t.Fatalf("insert project: %v", err)
		}
		if err := tx.Get(&componentID, `INSERT INTO components (org_id, project_id, slug, name, kind) VALUES ($1,$2,'c','billing-api','backend') RETURNING id`, orgID, projectID); err != nil {
			t.Fatalf("insert component: %v", err)
		}
	})

	form := url.Values{"kind": {"gitlab"}, "connection_id": {"new"}, "new_connection_name": {"gitlab.com"}}
	req := httptest.NewRequest(http.MethodPost, "http://"+host+"/components/"+componentID+"/bind", strings.NewReader(form.Encode()))
	req.Host = host
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "lotus_session", Value: raw})

	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "external key is required") {
		t.Fatalf("body missing the validation error:\n%s", body)
	}

	var bindingCount int
	withOrgTx(t, db, orgID, func(tx *sqlx.Tx) {
		if err := tx.Get(&bindingCount, `SELECT count(*) FROM metric_bindings WHERE component_id=$1`, componentID); err != nil {
			t.Fatalf("count: %v", err)
		}
	})
	if bindingCount != 0 {
		t.Fatalf("bindings = %d, want 0 (nothing should be created on a validation error)", bindingCount)
	}
}

func TestComponentPage_EnableDisableDeleteBinding(t *testing.T) {
	app, db := newDashboardTestApp(t)
	orgID, userID := seedMember(t, db, "acme-bindmgmt", "owner")
	raw := seedSession(t, db, orgID, userID)
	host := "acme-bindmgmt." + dashboardTestDomain

	var projectID, componentID, connID, bindingID string
	withOrgTx(t, db, orgID, func(tx *sqlx.Tx) {
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
	})

	postForm := func(path string) *http.Response {
		req := httptest.NewRequest(http.MethodPost, "http://"+host+path, nil)
		req.Host = host
		req.AddCookie(&http.Cookie{Name: "lotus_session", Value: raw})
		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("POST %s: %v", path, err)
		}
		return resp
	}

	disableResp := postForm("/bindings/" + bindingID + "/disable-ui")
	disableResp.Body.Close()
	if disableResp.StatusCode != http.StatusFound {
		t.Fatalf("disable status = %d, want 302", disableResp.StatusCode)
	}
	var enabled bool
	withOrgTx(t, db, orgID, func(tx *sqlx.Tx) {
		if err := tx.Get(&enabled, `SELECT enabled FROM metric_bindings WHERE id=$1`, bindingID); err != nil {
			t.Fatalf("read enabled: %v", err)
		}
	})
	if enabled {
		t.Fatal("enabled = true after disable-ui, want false")
	}

	deleteResp := postForm("/bindings/" + bindingID + "/delete-ui")
	deleteResp.Body.Close()
	if deleteResp.StatusCode != http.StatusFound {
		t.Fatalf("delete status = %d, want 302", deleteResp.StatusCode)
	}
	var deletedAt *string
	withOrgTx(t, db, orgID, func(tx *sqlx.Tx) {
		if err := tx.Get(&deletedAt, `SELECT deleted_at::text FROM metric_bindings WHERE id=$1`, bindingID); err != nil {
			t.Fatalf("read deleted_at: %v", err)
		}
	})
	if deletedAt == nil {
		t.Fatal("deleted_at is still null after delete-ui, want it set (soft delete)")
	}
}
