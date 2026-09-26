package web

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

func seedFiledProject(t *testing.T, db *sqlx.DB, orgID uuid.UUID, folderName, projectName, status string, score int) {
	t.Helper()
	withOrgTx(t, db, orgID, func(tx *sqlx.Tx) {
		var folderID, projectID, componentID uuid.UUID
		if err := tx.Get(&folderID, `INSERT INTO folders (org_id, name) VALUES ($1, $2) RETURNING id`, orgID, folderName); err != nil {
			t.Fatalf("insert folder: %v", err)
		}
		if err := tx.Get(&projectID, `INSERT INTO projects (org_id, slug, name, folder_id) VALUES ($1, $2, $2, $3) RETURNING id`, orgID, projectName, folderID); err != nil {
			t.Fatalf("insert project: %v", err)
		}
		if err := tx.Get(&componentID, `INSERT INTO components (org_id, project_id, slug, name, kind) VALUES ($1,$2,'c','api','backend') RETURNING id`, orgID, projectID); err != nil {
			t.Fatalf("insert component: %v", err)
		}
		if _, err := tx.Exec(`
			INSERT INTO component_metrics_current (org_id, project_id, component_id, metrics, status, score, updated_at)
			VALUES ($1,$2,$3, jsonb_build_object('sonarqube', jsonb_build_object('status', $4::text, 'score', $5::int)), $4, $5, now())`,
			orgID, projectID, componentID, status, score); err != nil {
			t.Fatalf("insert metric: %v", err)
		}
	})
}

func getProjectsPage(t *testing.T, subdomain, role string) (string, *sqlx.DB, uuid.UUID) {
	t.Helper()
	app, db := newDashboardTestApp(t)
	orgID, userID := seedMember(t, db, subdomain, role)
	raw := seedSession(t, db, orgID, userID)
	seedFiledProject(t, db, orgID, `Ops <b>"R&D"</b>'s`, "Linknet", "pass", 88)

	resp, err := app.Test(reqWithSession(http.MethodGet, subdomain+"."+dashboardTestDomain, "/projects", raw))
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /projects = %v, %v", resp, err)
	}
	body, _ := io.ReadAll(resp.Body)
	return string(body), db, orgID
}

func TestProjectsPage_RendersTheFolderTree(t *testing.T) {
	body, _, _ := getProjectsPage(t, "acme-tree", "owner")
	for _, want := range []string{`x-data="lotusTree()"`, `data-folder="1"`, "1 / 1 passing", "Linknet", "New folder", "folder-edit-"} {
		if !strings.Contains(body, want) {
			t.Fatalf("page missing %q", want)
		}
	}
}

func TestProjectsPage_ViewerSeesNoFolderControls(t *testing.T) {
	body, _, _ := getProjectsPage(t, "acme-tree-v", "viewer")
	if !strings.Contains(body, `data-folder="1"`) {
		t.Fatal("viewer does not see the folder tree")
	}
	for _, unwanted := range []string{"New folder", "folder-edit-", "Folder actions"} {
		if strings.Contains(body, unwanted) {
			t.Fatalf("viewer page contains %q", unwanted)
		}
	}
}

// Review focus: a folder name with markup and quotes is escaped everywhere
// it lands — row, dialog title, data attributes.
func TestProjectsPage_EscapesFolderNames(t *testing.T) {
	body, _, _ := getProjectsPage(t, "acme-tree-x", "owner")
	if strings.Contains(body, `<b>"R&D"</b>`) || strings.Contains(body, `Ops <b>`) {
		t.Fatal("folder name rendered unescaped")
	}
	if !strings.Contains(body, "Ops &lt;b&gt;") {
		t.Fatal("escaped folder name not found")
	}
}
