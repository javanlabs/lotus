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

// Review focus: Enter on the nested "Folder actions" button must not also
// toggle the row's collapse state — see the fix-round-1 report. The row's
// keydown handler must be scoped to itself with .self, not just .prevent.
func TestProjectsPage_FolderRowEnterKeyDoesNotFireOnActionsButton(t *testing.T) {
	body, _, _ := getProjectsPage(t, "acme-tree-kbd", "owner")
	if !strings.Contains(body, `@keydown.enter.self.prevent="toggle($el.dataset.node)"`) {
		t.Fatal("folder row keydown handler is missing .self — a nested button's bubbled Enter would incorrectly toggle the folder")
	}
}

// Review focus (final review, item 7): a folder row is a collapse toggle,
// so it needs aria-expanded to say which state it's in — bound as a
// boolean expression, per AGENTS.md's rule that :style (and, by the same
// logic, any :attr) must not be a plain string when it needs to react to
// state.
func TestProjectsPage_FolderRowHasAriaExpanded(t *testing.T) {
	body, _, _ := getProjectsPage(t, "acme-tree-aria", "owner")
	if !strings.Contains(body, `:aria-expanded="!isCollapsed($el.dataset.node)"`) {
		t.Fatal("folder row is missing :aria-expanded bound to its collapsed state")
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

// Review focus: a folder row's subtext ("N projects · failing: X 42") is a
// full sentence, much longer than a project row's ("1 component"). Giving
// it `flex:none` let it claim its full width unconditionally, which forced
// the flexbox shrink algorithm to take the room back from the *name* span
// instead — squeezing a folder's own name to zero width whenever the
// subtext was long enough (worse the deeper a folder is nested, since
// IndentPx eats further into the same column). See task-9-report.md for
// the reproduction. The fix: the name span keeps its content size
// (flex:0 1 auto, min-width:0) and only shrinks if truly out of room,
// while the subtext span (flex:1 1 0, min-width:0) is the one that yields
// space — and truncates itself via its own ellipsis — first.
func TestProjectsPage_FolderNameSpanIsProtectedFromSubtextSqueeze(t *testing.T) {
	body, _, _ := getProjectsPage(t, "acme-tree-flex", "owner")
	if strings.Contains(body, `color:#9CA3AF;flex:none;overflow:hidden;text-overflow:ellipsis">`) {
		t.Fatal("a row's subtext span still has flex:none — it will claim its full width and squeeze the name span to zero")
	}
	if !strings.Contains(body, `font-weight:700;color:#43284E;flex:0 1 auto;min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap">`) {
		t.Fatal("folder row's name span is missing flex:0 1 auto;min-width:0 — it can be squeezed to zero width by its own subtext")
	}
	if !strings.Contains(body, `color:#9CA3AF;flex:1 1 0;min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap">`) {
		t.Fatal("a row's subtext span is missing flex:1 1 0;min-width:0 — it should yield space (and self-truncate) before the name does")
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
