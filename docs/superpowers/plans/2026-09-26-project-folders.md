# Project Folders Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let an organization arrange its projects in one shared, nestable folder tree, where every folder shows how many of the projects beneath it pass their Lotus score.

**Architecture:** A new `folders` table (a parent-pointer tree, row-level-security scoped like every tenant table) and a nullable `projects.folder_id`. `internal/repo` enforces the tree rules (no cycles, max depth 8, only empty folders can be deleted) under a per-org advisory lock, and adds a pure `RollupFolders` that counts passing, failing and unmeasured projects at any depth. The `/projects` page builds the tree in memory from one folder query and renders it through the existing `dashboard_block`; Alpine handles collapse (remembered in `localStorage`) and tree-aware sorting.

**Tech Stack:** Go 1.26, Fiber v2, sqlx + pgx, goose migrations, Postgres 17 (dev), `html/template`, Alpine.js, htmx 2, Playwright.

**Spec:** `docs/superpowers/specs/2026-09-26-project-folders-design.md`

## Global Constraints

- Postgres 15 or later (`NULLS NOT DISTINCT`). Dev runs 17; confirm production before shipping.
- Folder nesting depth is at most **8** (`repo.MaxFolderDepth`). A top-level folder has depth 1.
- Folder names are trimmed, must not be empty, and are at most **80** characters (`repo.MaxFolderNameLength`). Sibling names are unique case-insensitively, including at the top level.
- A folder is green at **80%** or more of its measured projects passing (`rateTone`, the same threshold as the KPI cards), otherwise red. Its status dot is red if any project beneath it fails.
- A project **passes** when its Lotus status (from `repo.RollupProject`) is `pass`. Folders count every project at any depth. Unknown projects are left out of the share but shown as "N not measured".
- The org-wide score card and KPI numbers must not change when projects move between folders.
- Integrations are always ordered by `bindableKinds` (SonarQube, Trivy, Sentry, GitLab). See AGENTS.md.
- Alpine `:style` uses the object form, never a string (AGENTS.md).
- Dropdowns use the `lotus_select` component, never a native `<select>`.
- Permissions: `folder.create` and `folder.update` for owner, admin and member; `folder.delete` for owner and admin; moving a project needs `project.update`.
- Error copy, verbatim: "Can't move a folder into itself.", "Folders can nest at most 8 levels.", "There's already a folder called '<name>' here.", "'<name>' still contains … ." (built by `FolderNotEmptyError.Error()`).

## Deviations from the spec (confirm during review)

1. **Routes are form POSTs, not `PATCH`/`DELETE` JSON.** Every screen in this app posts HTML forms and uses htmx for errors shown inside a dialog; nothing sends `PATCH` or `DELETE`. So the routes are `POST /folders`, `POST /folders/:id/rename`, `POST /folders/:id/move`, `POST /folders/:id/delete`, and `POST /projects/:id/move`, with the spec's permissions. The existing JSON `PATCH /projects/:id` is left alone.
2. **Foreign keys use the default `NO ACTION`, not `RESTRICT`.** `RESTRICT` is checked mid-statement and would block deleting an organization (the organization's cascade deletes parent folders while their children still exist). `NO ACTION` is checked at the end of the statement: deleting a non-empty folder is still refused, but the organization cascade works. Task 2 tests this.
3. **Projects are moved from the project page** ("Move to folder" in its header) and from a picker on the New project form, not from a row menu. A project row is a link (`<a>`), and a menu nested inside a link is invalid HTML.
4. **Projects with no components now appear on `/projects`.** Today a project only gets a row once it has a component. A project filed in a folder must be visible, so every active project gets a row, showing "0 components" and no verdict.
5. **Names are capped at 80 characters**, and the not-empty message lists at most 5 project names, then "+N more".

## Review Focus

1. **A folder name containing HTML or quotes** (`Ops <b>"R&D"</b>'s`) must render escaped everywhere: row, dialog title, `data-*` attributes and the toast. Pinned in Task 7.
2. **Deleting an organization** that has nested folders and filed projects must succeed, not fail on the folder foreign keys. Pinned in Task 2.
3. **`localStorage` unavailable or throwing** (private window, blocked site data): the tree must still render and collapse, just without remembering. Pinned in Task 9.
4. **Two people moving folders at the same moment** so that together they would form a loop: exactly one move must be refused. Pinned in Task 3.
5. **A project that is filed and then archived** keeps its `folder_id`: it leaves the tree and the folder counts, still blocks deleting the folder (named, with "archived"), and returns to the same folder when unarchived. Pinned in Tasks 2 and 3.

## Running tests

Set this once per shell. `make test` sets it for you, but running a single test needs it:

```bash
export TEST_DATABASE_URL='postgres://postgres@localhost:5418/lotus_test?sslmode=disable'
```

The test template database is rebuilt automatically whenever the migrations change (`dbtest` keys it on `db.MigrationsChecksum()`).

## File Structure

| File | Responsibility |
|---|---|
| `internal/db/migrations/00026_folders.sql` (new) | `folders` table, RLS, `projects.folder_id`, permission seed |
| `internal/authz/principal.go` (modify) | `folder.*` permissions in the Go matrix |
| `internal/repo/folders.go` (new) | `Folder` type; create, list, rename, move, delete; `MoveProject`; tree rules |
| `internal/repo/projects.go` (modify) | `Project.FolderID`, `NewProject.FolderID`, column list |
| `internal/repo/folder_rollup.go` (new) | Pure `RollupFolders`: pass share per folder at any depth |
| `internal/web/foldertree.go` (new) | Tree assembly for the view model: rows, folder options, move targets |
| `internal/web/dashboardview.go` (modify) | New row/VM fields; `buildProjectDashboardViewModel` builds the tree |
| `internal/web/folders.go` (new) | Folder and project-move form handlers, audit events, error copy |
| `internal/web/flash.go` (modify) | Folder flash codes |
| `internal/web/server.go` (modify) | Register folder routes |
| `internal/web/dashboard.go`, `project_page.go`, `project_new.go` (modify) | Load folders, permissions, pickers |
| `web/templates/components/dashboard_block.html` (modify) | Folder rows, tree attributes |
| `web/templates/components/folder_dialogs.html` (new) | New-folder form and per-folder actions dialog |
| `web/templates/components/form_error.html` (new) | Dialog error fragment for htmx |
| `web/templates/components/select.html` (modify) | Optional `ID` so several pickers can share a field name |
| `web/templates/pages/projects.html`, `project.html`, `project_new.html` (modify) | Buttons, dialogs, pickers |
| `web/static/js/lotus.js` (modify) | `lotusTree()`; tree-aware `lotusSort.apply` |
| `e2e/tests/folders.spec.ts` (new) | Browser golden path and blocked-storage path |

---

### Task 1: Schema and permissions

**Files:**
- Create: `internal/db/migrations/00026_folders.sql`
- Modify: `internal/authz/principal.go:74-108` (the `rolePermissions` map)
- Test: `internal/authz/principal_test.go:13-40` (the `permissionMatrix`), plus the existing `internal/db/role_permissions_test.go`

**Interfaces:**
- Produces: table `folders(id, org_id, parent_id, name, sort_order, created_at)`; column `projects.folder_id uuid NULL`; permissions `folder.create`, `folder.update`, `folder.delete`.

- [ ] **Step 1: Write the failing test**

In `internal/authz/principal_test.go`, add three entries to `permissionMatrix`, directly after the `"project.delete"` line:

```go
	"folder.create":           {"owner", "admin", "member"},
	"folder.update":           {"owner", "admin", "member"},
	"folder.delete":           {"owner", "admin"},
```

- [ ] **Step 2: Run the test and confirm it fails**

Run: `go test ./internal/authz -run TestPermissionMatrix -v`
Expected: FAIL with lines such as `owner.Can("folder.create") = false, want true`.

- [ ] **Step 3: Add the permissions to the Go matrix**

In `internal/authz/principal.go`, add the folder permissions to three roles, right after each role's project permissions:

```go
	"owner": setOf(
		"dashboard.read", "project.create", "project.update", "project.delete",
		"folder.create", "folder.update", "folder.delete",
		"component.create", "component.update", "component.delete",
```

```go
	"admin": setOf(
		"dashboard.read", "project.create", "project.update", "project.delete",
		"folder.create", "folder.update", "folder.delete",
		"component.create", "component.update", "component.delete",
```

```go
	"member": setOf(
		"dashboard.read", "project.create", "project.update",
		"folder.create", "folder.update",
		"component.create", "component.update",
```

`viewer` is unchanged.

- [ ] **Step 4: Run the authz test and confirm it passes**

Run: `go test ./internal/authz -v`
Expected: PASS.

- [ ] **Step 5: Run the seed-vs-matrix test and confirm it now fails**

Run: `go test ./internal/db -run TestRolePermissions -v`
Expected: FAIL. The seeded `role_permissions` table no longer matches the Go matrix: it is missing the `folder.*` rows, and the owner count differs.

- [ ] **Step 6: Write the migration**

Create `internal/db/migrations/00026_folders.sql`:

```sql
-- +goose Up
-- Folders arrange an organization's projects into one shared tree
-- (docs/superpowers/specs/2026-09-26-project-folders-design.md). They are
-- arrangement only: members, connections and settings stay org-wide.
--
-- Both foreign keys below are the default NO ACTION rather than RESTRICT.
-- The two refuse a non-empty folder's deletion identically, but RESTRICT is
-- checked mid-statement, so deleting an organization — whose cascade
-- removes a parent folder while its children are still present — would
-- fail. NO ACTION is checked at the end of the statement, after the whole
-- cascade has run.
--
-- The composite (id, org_id) keys follow the components pattern: a folder
-- can only have a parent, and a project can only be filed in a folder,
-- from its own organization, even through a bug that forgot a scope.
CREATE TABLE folders (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id      uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    parent_id   uuid,
    name        text NOT NULL CHECK (btrim(name) <> '' AND char_length(name) <= 80),
    sort_order  integer NOT NULL DEFAULT 0,
    created_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (id, org_id),
    FOREIGN KEY (parent_id, org_id) REFERENCES folders (id, org_id)
);

-- Sibling names are unique ignoring case, and NULLS NOT DISTINCT makes the
-- top level (parent_id IS NULL) one sibling group too. Postgres 15+.
CREATE UNIQUE INDEX folders_sibling_name
    ON folders (org_id, parent_id, lower(name)) NULLS NOT DISTINCT;

-- +goose StatementBegin
DO $$
BEGIN
    EXECUTE 'ALTER TABLE folders ENABLE ROW LEVEL SECURITY';
    EXECUTE 'ALTER TABLE folders FORCE ROW LEVEL SECURITY';
    EXECUTE 'CREATE POLICY tenant_isolation ON folders USING (org_id = NULLIF(current_setting(''app.org_id'', true), '''')::uuid) WITH CHECK (org_id = NULLIF(current_setting(''app.org_id'', true), '''')::uuid)';
END
$$;
-- +goose StatementEnd

GRANT SELECT, INSERT, UPDATE, DELETE ON folders TO lotus_app;

-- NULL = the project sits at the root, which is where every existing
-- project stays until someone files it.
ALTER TABLE projects ADD COLUMN folder_id uuid;
ALTER TABLE projects ADD CONSTRAINT projects_folder_id_fkey
    FOREIGN KEY (folder_id, org_id) REFERENCES folders (id, org_id);
CREATE INDEX idx_projects_folder ON projects (folder_id) WHERE folder_id IS NOT NULL;

-- The documented copy of the Go permission matrix
-- (internal/db.TestRolePermissionsSeedMatchesTheGoMatrix keeps them equal).
INSERT INTO role_permissions (role_id, permission)
SELECT r.id, p.permission
FROM roles r
JOIN (VALUES
    ('owner', 'folder.create'), ('owner', 'folder.update'), ('owner', 'folder.delete'),
    ('admin', 'folder.create'), ('admin', 'folder.update'), ('admin', 'folder.delete'),
    ('member', 'folder.create'), ('member', 'folder.update')
) AS p (role_key, permission) ON p.role_key = r.key
WHERE r.org_id IS NULL
ON CONFLICT DO NOTHING;

-- +goose Down
DELETE FROM role_permissions rp
USING roles r
WHERE rp.role_id = r.id
  AND r.org_id IS NULL
  AND rp.permission IN ('folder.create', 'folder.update', 'folder.delete');

ALTER TABLE projects DROP CONSTRAINT IF EXISTS projects_folder_id_fkey;
DROP INDEX IF EXISTS idx_projects_folder;
ALTER TABLE projects DROP COLUMN IF EXISTS folder_id;
DROP TABLE IF EXISTS folders;
```

- [ ] **Step 7: Run the database tests and confirm they pass**

Run: `go test ./internal/db -v`
Expected: PASS. This includes `TestRolePermissionsSeedMatchesTheGoMatrix` and the migrations' up/down/idempotency tests.

- [ ] **Step 8: Migrate the dev database**

Run: `make migrate-up`
Expected: the last lines show `OK   00026_folders.sql` and `goose: successfully migrated database to version: 26`.

- [ ] **Step 9: Commit**

```bash
git add internal/db/migrations/00026_folders.sql internal/authz/principal.go internal/authz/principal_test.go
git commit -m "feat(db,authz): add the folders table and folder permissions"
```

---

### Task 2: Repo — create, list and rename folders; file projects

**Files:**
- Create: `internal/repo/folders.go`
- Modify: `internal/repo/projects.go` (the `Project` and `NewProject` structs, every column list, and `CreateProject`'s error mapping)
- Test: `internal/repo/folders_test.go` (new)

**Interfaces:**
- Consumes: the `folders` table and `projects.folder_id` from Task 1.
- Produces:
  - `const MaxFolderDepth = 8`, `const MaxFolderNameLength = 80`
  - `var ErrFolderNameTaken, ErrFolderCycle, ErrFolderTooDeep error`
  - `type FolderNotEmptyError struct{ Name string; Subfolders int; Projects []string; Archived int }` with `Error() string` (fully used in Task 3)
  - `type Folder struct{ ID, OrgID uuid.UUID; ParentID *uuid.UUID; Name string; SortOrder int; CreatedAt time.Time }`
  - `func CreateFolder(ctx context.Context, s tenant.Scope, name string, parentID *uuid.UUID) (*Folder, error)`
  - `func ListFolders(ctx context.Context, s tenant.Scope) ([]Folder, error)` — ordered by `sort_order`, then `lower(name)`
  - `func FolderByID(ctx context.Context, s tenant.Scope, id uuid.UUID) (*Folder, error)`
  - `func RenameFolder(ctx context.Context, s tenant.Scope, id uuid.UUID, name string) (*Folder, error)`
  - `func MoveProject(ctx context.Context, s tenant.Scope, projectID uuid.UUID, folderID *uuid.UUID) (from *uuid.UUID, err error)`
  - `Project.FolderID *uuid.UUID` (`db:"folder_id"`), `NewProject.FolderID *uuid.UUID`

- [ ] **Step 1: Check that the helper names are free**

Run: `grep -rn "func pluralS\|func folderWriteErr\|func lockFolderTree\|func loadFolderParents\|func folderDepth" internal/repo`
Expected: no output.

- [ ] **Step 2: Write the failing tests**

Create `internal/repo/folders_test.go`:

```go
package repo_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/dbtest"
	"github.com/uyab/lotus/internal/repo"
	"github.com/uyab/lotus/internal/tenant"
)

// folderTestOrg inserts an organization and returns a scope bound to it,
// the same way a request or a worker run would be bound.
func folderTestOrg(t *testing.T, db *sqlx.DB, subdomain string) (uuid.UUID, tenant.Scope) {
	t.Helper()
	var orgID uuid.UUID
	if err := db.Get(&orgID, `INSERT INTO organizations (subdomain, name) VALUES ($1, $1) RETURNING id`, subdomain); err != nil {
		t.Fatalf("insert org: %v", err)
	}
	return orgID, tenant.FromRun(db, orgID)
}

func mustFolder(t *testing.T, s tenant.Scope, name string, parent *repo.Folder) *repo.Folder {
	t.Helper()
	var parentID *uuid.UUID
	if parent != nil {
		parentID = &parent.ID
	}
	f, err := repo.CreateFolder(context.Background(), s, name, parentID)
	if err != nil {
		t.Fatalf("create folder %q: %v", name, err)
	}
	return f
}

func mustProject(t *testing.T, s tenant.Scope, slug string, folder *repo.Folder) *repo.Project {
	t.Helper()
	in := repo.NewProject{Slug: slug, Name: slug}
	if folder != nil {
		in.FolderID = &folder.ID
	}
	p, err := repo.CreateProject(context.Background(), s, in)
	if err != nil {
		t.Fatalf("create project %q: %v", slug, err)
	}
	return p
}

func TestCreateFolder_TopLevelAndNested(t *testing.T) {
	db := dbtest.Open(t)
	_, s := folderTestOrg(t, db, "acme")
	it := mustFolder(t, s, "IT Services", nil)
	tribe := mustFolder(t, s, "Tribe lead A", it)

	folders, err := repo.ListFolders(context.Background(), s)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(folders) != 2 {
		t.Fatalf("listed %d folders, want 2", len(folders))
	}
	if it.ParentID != nil {
		t.Fatalf("top-level folder has parent %v", it.ParentID)
	}
	if tribe.ParentID == nil || *tribe.ParentID != it.ID {
		t.Fatalf("Tribe lead A parent = %v, want %v", tribe.ParentID, it.ID)
	}
}

func TestCreateFolder_TrimsAndRejectsBlankOrLongNames(t *testing.T) {
	db := dbtest.Open(t)
	_, s := folderTestOrg(t, db, "acme")
	ctx := context.Background()

	f, err := repo.CreateFolder(ctx, s, "  Product  ", nil)
	if err != nil || f.Name != "Product" {
		t.Fatalf("create trimmed = %v, %v; want name %q", f, err, "Product")
	}
	if _, err := repo.CreateFolder(ctx, s, "   ", nil); !errors.Is(err, repo.ErrInvalidInput) {
		t.Fatalf("blank name err = %v, want ErrInvalidInput", err)
	}
	if _, err := repo.CreateFolder(ctx, s, strings.Repeat("x", repo.MaxFolderNameLength), nil); err != nil {
		t.Fatalf("80-char name err = %v, want nil", err)
	}
	if _, err := repo.CreateFolder(ctx, s, strings.Repeat("y", repo.MaxFolderNameLength+1), nil); !errors.Is(err, repo.ErrInvalidInput) {
		t.Fatalf("81-char name err = %v, want ErrInvalidInput", err)
	}
}

func TestCreateFolder_SiblingNamesAreUniqueIgnoringCaseIncludingTopLevel(t *testing.T) {
	db := dbtest.Open(t)
	_, s := folderTestOrg(t, db, "acme")
	ctx := context.Background()
	product := mustFolder(t, s, "Product", nil)

	if _, err := repo.CreateFolder(ctx, s, "product", nil); !errors.Is(err, repo.ErrFolderNameTaken) {
		t.Fatalf("duplicate top-level name err = %v, want ErrFolderNameTaken", err)
	}
	mustFolder(t, s, "Archive", product)
	if _, err := repo.CreateFolder(ctx, s, "ARCHIVE", &product.ID); !errors.Is(err, repo.ErrFolderNameTaken) {
		t.Fatalf("duplicate nested name err = %v, want ErrFolderNameTaken", err)
	}
	// The same name under a different parent is fine.
	mustFolder(t, s, "Archive", nil)
}

func TestCreateFolder_DepthCapIsEightLevels(t *testing.T) {
	db := dbtest.Open(t)
	_, s := folderTestOrg(t, db, "acme")
	var parent *repo.Folder
	for i := 1; i <= repo.MaxFolderDepth; i++ {
		parent = mustFolder(t, s, "level", parent)
	}
	if _, err := repo.CreateFolder(context.Background(), s, "too deep", &parent.ID); !errors.Is(err, repo.ErrFolderTooDeep) {
		t.Fatalf("ninth level err = %v, want ErrFolderTooDeep", err)
	}
}

func TestFolders_AreIsolatedBetweenOrganizations(t *testing.T) {
	db := dbtest.Open(t)
	_, acme := folderTestOrg(t, db, "acme")
	_, other := folderTestOrg(t, db, "other")
	ctx := context.Background()
	acmeFolder := mustFolder(t, acme, "Product", nil)

	if folders, err := repo.ListFolders(ctx, other); err != nil || len(folders) != 0 {
		t.Fatalf("other org lists %d folders (err %v), want 0", len(folders), err)
	}
	if _, err := repo.CreateFolder(ctx, other, "Sneaky", &acmeFolder.ID); !errors.Is(err, repo.ErrNotFound) {
		t.Fatalf("parent from another org err = %v, want ErrNotFound", err)
	}
	if _, err := repo.RenameFolder(ctx, other, acmeFolder.ID, "Mine now"); !errors.Is(err, repo.ErrNotFound) {
		t.Fatalf("rename across orgs err = %v, want ErrNotFound", err)
	}
	p := mustProject(t, other, "theirs", nil)
	if _, err := repo.MoveProject(ctx, other, p.ID, &acmeFolder.ID); !errors.Is(err, repo.ErrNotFound) {
		t.Fatalf("filing into another org's folder err = %v, want ErrNotFound", err)
	}
	if _, err := repo.CreateProject(ctx, other, repo.NewProject{Slug: "x", Name: "x", FolderID: &acmeFolder.ID}); !errors.Is(err, repo.ErrNotFound) {
		t.Fatalf("creating a project in another org's folder err = %v, want ErrNotFound", err)
	}
}

func TestRenameFolder(t *testing.T) {
	db := dbtest.Open(t)
	_, s := folderTestOrg(t, db, "acme")
	ctx := context.Background()
	mustFolder(t, s, "Product", nil)
	it := mustFolder(t, s, "IT", nil)

	renamed, err := repo.RenameFolder(ctx, s, it.ID, " IT Services ")
	if err != nil || renamed.Name != "IT Services" {
		t.Fatalf("rename = %v, %v; want IT Services", renamed, err)
	}
	if _, err := repo.RenameFolder(ctx, s, it.ID, "PRODUCT"); !errors.Is(err, repo.ErrFolderNameTaken) {
		t.Fatalf("rename onto a sibling's name err = %v, want ErrFolderNameTaken", err)
	}
	if _, err := repo.RenameFolder(ctx, s, uuid.New(), "Ghost"); !errors.Is(err, repo.ErrNotFound) {
		t.Fatalf("rename unknown err = %v, want ErrNotFound", err)
	}
}

func TestMoveProject_IntoAFolderAndBackToTheRoot(t *testing.T) {
	db := dbtest.Open(t)
	_, s := folderTestOrg(t, db, "acme")
	ctx := context.Background()
	product := mustFolder(t, s, "Product", nil)
	p := mustProject(t, s, "alurkerja", nil)

	from, err := repo.MoveProject(ctx, s, p.ID, &product.ID)
	if err != nil || from != nil {
		t.Fatalf("first move = from %v, err %v; want from nil", from, err)
	}
	got, _ := repo.ProjectByID(ctx, s, p.ID)
	if got.FolderID == nil || *got.FolderID != product.ID {
		t.Fatalf("FolderID = %v, want %v", got.FolderID, product.ID)
	}

	from, err = repo.MoveProject(ctx, s, p.ID, nil)
	if err != nil || from == nil || *from != product.ID {
		t.Fatalf("move to root = from %v, err %v; want from %v", from, err, product.ID)
	}
	got, _ = repo.ProjectByID(ctx, s, p.ID)
	if got.FolderID != nil {
		t.Fatalf("FolderID = %v after moving to the root, want nil", got.FolderID)
	}
	if _, err := repo.MoveProject(ctx, s, uuid.New(), &product.ID); !errors.Is(err, repo.ErrNotFound) {
		t.Fatalf("moving an unknown project err = %v, want ErrNotFound", err)
	}
}

func TestCreateProject_CanBeFiledStraightIntoAFolder(t *testing.T) {
	db := dbtest.Open(t)
	_, s := folderTestOrg(t, db, "acme")
	product := mustFolder(t, s, "Product", nil)
	p := mustProject(t, s, "syarq", product)
	if p.FolderID == nil || *p.FolderID != product.ID {
		t.Fatalf("FolderID = %v, want %v", p.FolderID, product.ID)
	}
}

// Review focus: a filed project that is archived keeps its folder, so
// unarchiving puts it back where it was.
func TestArchivingAProjectKeepsItsFolder(t *testing.T) {
	db := dbtest.Open(t)
	_, s := folderTestOrg(t, db, "acme")
	ctx := context.Background()
	product := mustFolder(t, s, "Product", nil)
	p := mustProject(t, s, "aos", product)

	if err := repo.SetProjectArchived(ctx, s, p.ID, true); err != nil {
		t.Fatalf("archive: %v", err)
	}
	if err := repo.SetProjectArchived(ctx, s, p.ID, false); err != nil {
		t.Fatalf("unarchive: %v", err)
	}
	got, _ := repo.ProjectByID(ctx, s, p.ID)
	if got.FolderID == nil || *got.FolderID != product.ID {
		t.Fatalf("FolderID after archive round trip = %v, want %v", got.FolderID, product.ID)
	}
}

// Review focus: deleting an organization must cascade through nested
// folders and filed projects — the reason the folder keys are NO ACTION.
func TestDeletingAnOrganizationCascadesThroughNestedFolders(t *testing.T) {
	db := dbtest.Open(t)
	orgID, s := folderTestOrg(t, db, "acme")
	it := mustFolder(t, s, "IT Services", nil)
	tribe := mustFolder(t, s, "Tribe lead A", it)
	mustProject(t, s, "linknet", tribe)

	if _, err := db.Exec(`DELETE FROM organizations WHERE id = $1`, orgID); err != nil {
		t.Fatalf("delete organization: %v", err)
	}
}
```

- [ ] **Step 3: Run the tests and confirm they fail to compile**

Run: `go test ./internal/repo -run 'Folder|MoveProject|CreateProject_Can|Archiving|DeletingAnOrganization' -v`
Expected: FAIL (build error), with messages like `undefined: repo.CreateFolder` and `unknown field FolderID in struct literal`.

- [ ] **Step 4: Add `FolderID` to projects**

In `internal/repo/projects.go`:

1. Add the field to `Project`, after `CreatedAt`:

```go
	// FolderID is the folder the project is filed in, nil at the root.
	FolderID *uuid.UUID `db:"folder_id"`
```

2. Add the field to `NewProject`:

```go
type NewProject struct {
	Slug        string
	Name        string
	Description string
	FolderID    *uuid.UUID
}
```

3. Add a column-list constant under the `Project` type, and use it in `CreateProject`'s `RETURNING`, `ProjectByID`'s `SELECT`, `ListProjects`' `SELECT` and `UpdateProject`'s `RETURNING`, in place of the repeated literal `id, org_id, slug, name, description, archived_at, sort_order, created_at`:

```go
// projectCols is every Project column, in struct order, so the queries
// below cannot drift apart when a column is added.
const projectCols = `id, org_id, slug, name, description, archived_at, sort_order, created_at, folder_id`
```

For example, `ProjectByID` becomes:

```go
		return tx.GetContext(ctx, &out, `SELECT `+projectCols+` FROM projects WHERE id = $1`, id)
```

`ListProjects` becomes:

```go
		q := `SELECT ` + projectCols + ` FROM projects`
```

4. Replace `CreateProject` with this version (it inserts `folder_id` and maps a foreign-key violation, meaning a folder this org can't see, to `ErrNotFound`):

```go
func CreateProject(ctx context.Context, s tenant.Scope, in NewProject) (*Project, error) {
	var out Project
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.GetContext(ctx, &out, `
			INSERT INTO projects (org_id, slug, name, description, folder_id)
			VALUES ($1, $2, $3, $4, $5)
			RETURNING `+projectCols,
			s.OrgID(), in.Slug, in.Name, in.Description, in.FolderID)
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) {
			switch pgErr.Code {
			case "23505":
				return nil, ErrSlugTaken
			case "23503":
				// folder_id names a folder this organization cannot see.
				return nil, ErrNotFound
			}
		}
		return nil, err
	}
	return &out, nil
}
```

- [ ] **Step 5: Implement the folder repo**

Create `internal/repo/folders.go`:

```go
package repo

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/tenant"
)

// MaxFolderDepth is how many levels folders may nest; a top-level folder
// is level 1. Deep enough for division → tribe lead → squad and more, and
// shallow enough that the indented tree stays readable.
const MaxFolderDepth = 8

// MaxFolderNameLength bounds a folder name, in characters. The database
// CHECK enforces the same number.
const MaxFolderNameLength = 80

var (
	// ErrFolderNameTaken: a sibling folder already has this name, ignoring
	// case. Top-level folders are one sibling group.
	ErrFolderNameTaken = errors.New("a folder with that name already exists here")
	// ErrFolderCycle: the move would put a folder inside itself.
	ErrFolderCycle = errors.New("a folder cannot be moved into itself or its own subfolder")
	// ErrFolderTooDeep: the create or move would nest past MaxFolderDepth.
	ErrFolderTooDeep = errors.New("folders cannot nest that deep")
)

// FolderNotEmptyError refuses deleting a folder that still holds anything.
// Archived projects count: they keep their folder so unarchiving can put
// them back, so a folder that looks empty can still hold them — which is
// why the message names every project, archived or not.
type FolderNotEmptyError struct {
	Name       string
	Subfolders int
	Projects   []string // every project filed here, archived included, by name
	Archived   int      // how many of Projects are archived
}

// maxNamedInError caps how many project names the message lists.
const maxNamedInError = 5

func (e *FolderNotEmptyError) Error() string {
	var parts []string
	if e.Subfolders > 0 {
		parts = append(parts, fmt.Sprintf("%d folder%s", e.Subfolders, pluralS(e.Subfolders)))
	}
	if n := len(e.Projects); n > 0 {
		names, more := e.Projects, ""
		if n > maxNamedInError {
			names, more = names[:maxNamedInError], fmt.Sprintf(" +%d more", n-maxNamedInError)
		}
		p := fmt.Sprintf("%d project%s (%s%s", n, pluralS(n), strings.Join(names, ", "), more)
		if e.Archived > 0 {
			p += fmt.Sprintf(" — %d archived", e.Archived)
		}
		parts = append(parts, p+")")
	}
	if len(parts) == 0 {
		return fmt.Sprintf("'%s' is not empty.", e.Name)
	}
	return fmt.Sprintf("'%s' still contains %s.", e.Name, strings.Join(parts, " and "))
}

func pluralS(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// Folder is one node of an organization's project tree.
type Folder struct {
	ID        uuid.UUID  `db:"id"`
	OrgID     uuid.UUID  `db:"org_id"`
	ParentID  *uuid.UUID `db:"parent_id"` // nil at the top level
	Name      string     `db:"name"`
	SortOrder int        `db:"sort_order"`
	CreatedAt time.Time  `db:"created_at"`
}

const folderCols = `id, org_id, parent_id, name, sort_order, created_at`

func normalizeFolderName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > MaxFolderNameLength {
		return "", ErrInvalidInput
	}
	return name, nil
}

// folderWriteErr maps the constraint violations a folder write can hit to
// the package's sentinels.
func folderWriteErr(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505":
			return ErrFolderNameTaken
		case "23503":
			// A parent or folder this organization cannot see: RLS hides
			// the row, the composite foreign key refuses the reference.
			return ErrNotFound
		case "23514":
			return ErrInvalidInput
		}
	}
	if isNoRows(err) {
		return ErrNotFound
	}
	return err
}

// lockFolderTree serializes every change to the shape of one
// organization's tree for the rest of the transaction. The cycle and depth
// checks read the whole tree and then write; without the lock two moves
// that are each valid alone could together form a loop.
func lockFolderTree(ctx context.Context, tx *sqlx.Tx, orgID uuid.UUID) error {
	_, err := tx.ExecContext(ctx,
		`SELECT pg_advisory_xact_lock(hashtextextended('lotus.folders:' || $1::text, 0))`, orgID.String())
	return err
}

// loadFolderParents reads the organization's whole tree as child → parent.
// An organization has tens of folders, so reading them all is cheaper and
// simpler than a recursive query per check.
func loadFolderParents(ctx context.Context, tx *sqlx.Tx) (map[uuid.UUID]*uuid.UUID, error) {
	var rows []struct {
		ID       uuid.UUID  `db:"id"`
		ParentID *uuid.UUID `db:"parent_id"`
	}
	if err := tx.SelectContext(ctx, &rows, `SELECT id, parent_id FROM folders`); err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID]*uuid.UUID, len(rows))
	for _, r := range rows {
		out[r.ID] = r.ParentID
	}
	return out, nil
}

// folderDepth is id's level: 1 at the top. The step bound means even a
// corrupt loop in the data cannot hang the walk.
func folderDepth(parents map[uuid.UUID]*uuid.UUID, id uuid.UUID) int {
	depth := 0
	for cur := &id; cur != nil && depth <= len(parents); cur = parents[*cur] {
		depth++
	}
	return depth
}

func CreateFolder(ctx context.Context, s tenant.Scope, name string, parentID *uuid.UUID) (*Folder, error) {
	name, err := normalizeFolderName(name)
	if err != nil {
		return nil, err
	}
	var out Folder
	err = s.Tx(ctx, func(tx *sqlx.Tx) error {
		if err := lockFolderTree(ctx, tx, s.OrgID()); err != nil {
			return err
		}
		if parentID != nil {
			parents, err := loadFolderParents(ctx, tx)
			if err != nil {
				return err
			}
			if _, ok := parents[*parentID]; !ok {
				return ErrNotFound
			}
			if folderDepth(parents, *parentID)+1 > MaxFolderDepth {
				return ErrFolderTooDeep
			}
		}
		return tx.GetContext(ctx, &out, `
			INSERT INTO folders (org_id, parent_id, name) VALUES ($1, $2, $3)
			RETURNING `+folderCols, s.OrgID(), parentID, name)
	})
	if err != nil {
		return nil, folderWriteErr(err)
	}
	return &out, nil
}

// ListFolders returns every folder in the organization. The caller builds
// the tree; the order here is only the order siblings are shown in.
func ListFolders(ctx context.Context, s tenant.Scope) ([]Folder, error) {
	var out []Folder
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &out,
			`SELECT `+folderCols+` FROM folders ORDER BY sort_order, lower(name)`)
	})
	return out, err
}

func FolderByID(ctx context.Context, s tenant.Scope, id uuid.UUID) (*Folder, error) {
	var out Folder
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.GetContext(ctx, &out, `SELECT `+folderCols+` FROM folders WHERE id = $1`, id)
	})
	if err != nil {
		return nil, folderWriteErr(err)
	}
	return &out, nil
}

func RenameFolder(ctx context.Context, s tenant.Scope, id uuid.UUID, name string) (*Folder, error) {
	name, err := normalizeFolderName(name)
	if err != nil {
		return nil, err
	}
	var out Folder
	err = s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.GetContext(ctx, &out,
			`UPDATE folders SET name = $2 WHERE id = $1 RETURNING `+folderCols, id, name)
	})
	if err != nil {
		return nil, folderWriteErr(err)
	}
	return &out, nil
}

// MoveProject files a project in folderID, or at the root when folderID is
// nil, and returns the folder it was in before (nil for the root).
func MoveProject(ctx context.Context, s tenant.Scope, projectID uuid.UUID, folderID *uuid.UUID) (*uuid.UUID, error) {
	var from *uuid.UUID
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		if err := tx.GetContext(ctx, &from,
			`SELECT folder_id FROM projects WHERE id = $1 FOR UPDATE`, projectID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE projects SET folder_id = $2 WHERE id = $1`, projectID, folderID)
		return err
	})
	if err != nil {
		return nil, folderWriteErr(err)
	}
	return from, nil
}
```

- [ ] **Step 6: Run the tests and confirm they pass**

Run: `go test ./internal/repo -run 'Folder|MoveProject|CreateProject_Can|Archiving|DeletingAnOrganization' -v`
Expected: PASS, every test.

- [ ] **Step 7: Run the whole repo package and build everything**

Run: `go build ./... && go test ./internal/repo`
Expected: PASS. The `Project` column-list change must not break any existing query.

- [ ] **Step 8: Commit**

```bash
git add internal/repo/folders.go internal/repo/folders_test.go internal/repo/projects.go
git commit -m "feat(repo): create, list and rename folders, and file projects in them"
```

---

### Task 3: Repo — move and delete folders

**Files:**
- Modify: `internal/repo/folders.go`
- Test: `internal/repo/folders_test.go`

**Interfaces:**
- Consumes: everything Task 2 produces.
- Produces:
  - `func MoveFolder(ctx context.Context, s tenant.Scope, id uuid.UUID, parentID *uuid.UUID) (*Folder, error)` — `parentID == nil` moves to the top level
  - `func DeleteFolder(ctx context.Context, s tenant.Scope, id uuid.UUID) (*Folder, error)` — returns the deleted folder (for the audit event); returns `*FolderNotEmptyError` when anything is inside

- [ ] **Step 1: Write the failing tests**

Append to `internal/repo/folders_test.go` (and add `"sync"` to its imports):

```go
func TestMoveFolder_RefusesCycles(t *testing.T) {
	db := dbtest.Open(t)
	_, s := folderTestOrg(t, db, "acme")
	ctx := context.Background()
	it := mustFolder(t, s, "IT Services", nil)
	tribe := mustFolder(t, s, "Tribe lead A", it)
	squad := mustFolder(t, s, "Squad 1", tribe)

	for _, target := range []*repo.Folder{it, tribe, squad} {
		if _, err := repo.MoveFolder(ctx, s, it.ID, &target.ID); !errors.Is(err, repo.ErrFolderCycle) {
			t.Fatalf("moving IT Services into %q err = %v, want ErrFolderCycle", target.Name, err)
		}
	}
}

func TestMoveFolder_ToAnotherParentAndBackToTheTop(t *testing.T) {
	db := dbtest.Open(t)
	_, s := folderTestOrg(t, db, "acme")
	ctx := context.Background()
	it := mustFolder(t, s, "IT Services", nil)
	tribe := mustFolder(t, s, "Tribe lead A", nil)

	moved, err := repo.MoveFolder(ctx, s, tribe.ID, &it.ID)
	if err != nil || moved.ParentID == nil || *moved.ParentID != it.ID {
		t.Fatalf("move under IT = %v, %v", moved, err)
	}
	moved, err = repo.MoveFolder(ctx, s, tribe.ID, nil)
	if err != nil || moved.ParentID != nil {
		t.Fatalf("move to top = %v, %v", moved, err)
	}
}

func TestMoveFolder_DepthCountsTheWholeSubtree(t *testing.T) {
	db := dbtest.Open(t)
	_, s := folderTestOrg(t, db, "acme")
	ctx := context.Background()

	// A chain six deep, and a separate folder that has one child
	// (a subtree two levels tall).
	chain := make([]*repo.Folder, 0, 7)
	var parent *repo.Folder
	for i := 1; i <= 7; i++ {
		parent = mustFolder(t, s, "level", parent)
		chain = append(chain, parent)
	}
	mover := mustFolder(t, s, "Mover", nil)
	mustFolder(t, s, "Child", mover)

	// Under level 7: 7 + 2 = 9 > 8.
	if _, err := repo.MoveFolder(ctx, s, mover.ID, &chain[6].ID); !errors.Is(err, repo.ErrFolderTooDeep) {
		t.Fatalf("move under level 7 err = %v, want ErrFolderTooDeep", err)
	}
	// Under level 6: 6 + 2 = 8, allowed.
	if _, err := repo.MoveFolder(ctx, s, mover.ID, &chain[5].ID); err != nil {
		t.Fatalf("move under level 6 err = %v, want nil", err)
	}
}

func TestMoveFolder_NameClashAtTheDestination(t *testing.T) {
	db := dbtest.Open(t)
	_, s := folderTestOrg(t, db, "acme")
	it := mustFolder(t, s, "IT Services", nil)
	mustFolder(t, s, "Archive", it)
	archive := mustFolder(t, s, "archive", nil)

	if _, err := repo.MoveFolder(context.Background(), s, archive.ID, &it.ID); !errors.Is(err, repo.ErrFolderNameTaken) {
		t.Fatalf("err = %v, want ErrFolderNameTaken", err)
	}
}

func TestMoveFolder_AcrossOrganizationsIsNotFound(t *testing.T) {
	db := dbtest.Open(t)
	_, acme := folderTestOrg(t, db, "acme")
	_, other := folderTestOrg(t, db, "other")
	ctx := context.Background()
	acmeFolder := mustFolder(t, acme, "Product", nil)
	otherFolder := mustFolder(t, other, "Mine", nil)

	if _, err := repo.MoveFolder(ctx, other, acmeFolder.ID, nil); !errors.Is(err, repo.ErrNotFound) {
		t.Fatalf("moving another org's folder err = %v, want ErrNotFound", err)
	}
	if _, err := repo.MoveFolder(ctx, other, otherFolder.ID, &acmeFolder.ID); !errors.Is(err, repo.ErrNotFound) {
		t.Fatalf("moving under another org's folder err = %v, want ErrNotFound", err)
	}
}

// Review focus: two people moving folders at the same moment so that each
// move is valid alone but together they form a loop. The advisory lock
// makes the second one see the first: exactly one must fail.
func TestMoveFolder_ConcurrentOppositeMovesCannotFormALoop(t *testing.T) {
	db := dbtest.Open(t)
	_, s := folderTestOrg(t, db, "acme")
	ctx := context.Background()

	for i := 0; i < 20; i++ {
		a := mustFolder(t, s, "A"+uuid.NewString()[:8], nil)
		b := mustFolder(t, s, "B"+uuid.NewString()[:8], nil)

		var wg sync.WaitGroup
		start := make(chan struct{})
		errs := make([]error, 2)
		wg.Add(2)
		go func() { defer wg.Done(); <-start; _, errs[0] = repo.MoveFolder(ctx, s, a.ID, &b.ID) }()
		go func() { defer wg.Done(); <-start; _, errs[1] = repo.MoveFolder(ctx, s, b.ID, &a.ID) }()
		close(start)
		wg.Wait()

		failed := 0
		for _, err := range errs {
			if err != nil {
				if !errors.Is(err, repo.ErrFolderCycle) {
					t.Fatalf("round %d: unexpected error %v", i, err)
				}
				failed++
			}
		}
		if failed != 1 {
			t.Fatalf("round %d: %d moves failed, want exactly 1 (errs %v)", i, failed, errs)
		}
	}
}

func TestDeleteFolder_EmptyFolderIsDeleted(t *testing.T) {
	db := dbtest.Open(t)
	_, s := folderTestOrg(t, db, "acme")
	ctx := context.Background()
	f := mustFolder(t, s, "Old", nil)

	deleted, err := repo.DeleteFolder(ctx, s, f.ID)
	if err != nil || deleted.Name != "Old" {
		t.Fatalf("delete = %v, %v", deleted, err)
	}
	if _, err := repo.FolderByID(ctx, s, f.ID); !errors.Is(err, repo.ErrNotFound) {
		t.Fatalf("lookup after delete err = %v, want ErrNotFound", err)
	}
	if _, err := repo.DeleteFolder(ctx, s, f.ID); !errors.Is(err, repo.ErrNotFound) {
		t.Fatalf("second delete err = %v, want ErrNotFound", err)
	}
}

func TestDeleteFolder_RefusesAFolderWithSubfolders(t *testing.T) {
	db := dbtest.Open(t)
	_, s := folderTestOrg(t, db, "acme")
	it := mustFolder(t, s, "IT Services", nil)
	mustFolder(t, s, "Tribe lead A", it)

	_, err := repo.DeleteFolder(context.Background(), s, it.ID)
	var notEmpty *repo.FolderNotEmptyError
	if !errors.As(err, &notEmpty) {
		t.Fatalf("err = %v, want *FolderNotEmptyError", err)
	}
	if want := "'IT Services' still contains 1 folder."; notEmpty.Error() != want {
		t.Fatalf("message = %q, want %q", notEmpty.Error(), want)
	}
}

// Review focus: a folder whose only project is archived looks empty on the
// page but must still refuse deletion, naming the archived project.
func TestDeleteFolder_RefusesAFolderHoldingOnlyAnArchivedProject(t *testing.T) {
	db := dbtest.Open(t)
	_, s := folderTestOrg(t, db, "acme")
	ctx := context.Background()
	tribe := mustFolder(t, s, "Tribe lead A", nil)
	p := mustProject(t, s, "Linknet", tribe)
	if err := repo.SetProjectArchived(ctx, s, p.ID, true); err != nil {
		t.Fatalf("archive: %v", err)
	}

	_, err := repo.DeleteFolder(ctx, s, tribe.ID)
	var notEmpty *repo.FolderNotEmptyError
	if !errors.As(err, &notEmpty) {
		t.Fatalf("err = %v, want *FolderNotEmptyError", err)
	}
	if want := "'Tribe lead A' still contains 1 project (Linknet — 1 archived)."; notEmpty.Error() != want {
		t.Fatalf("message = %q, want %q", notEmpty.Error(), want)
	}
}

func TestFolderNotEmptyError_CapsTheNamedProjects(t *testing.T) {
	e := &repo.FolderNotEmptyError{
		Name: "Big", Subfolders: 2,
		Projects: []string{"a", "b", "c", "d", "e", "f", "g"},
	}
	want := "'Big' still contains 2 folders and 7 projects (a, b, c, d, e +2 more)."
	if e.Error() != want {
		t.Fatalf("message = %q, want %q", e.Error(), want)
	}
}
```

- [ ] **Step 2: Run the tests and confirm they fail to compile**

Run: `go test ./internal/repo -run 'MoveFolder|DeleteFolder|FolderNotEmpty' -v`
Expected: FAIL (build error): `undefined: repo.MoveFolder`, `undefined: repo.DeleteFolder`.

- [ ] **Step 3: Implement move and delete**

Append to `internal/repo/folders.go`:

```go
// isFolderWithin reports whether candidate is root or sits anywhere
// beneath it — the cycle test for moving root under candidate.
func isFolderWithin(parents map[uuid.UUID]*uuid.UUID, candidate, root uuid.UUID) bool {
	cur := &candidate
	for steps := 0; cur != nil && steps <= len(parents); steps++ {
		if *cur == root {
			return true
		}
		cur = parents[*cur]
	}
	return false
}

// folderSubtreeHeight is how many levels id's subtree spans: 1 for a
// folder with no subfolders.
func folderSubtreeHeight(parents map[uuid.UUID]*uuid.UUID, id uuid.UUID) int {
	children := make(map[uuid.UUID][]uuid.UUID, len(parents))
	for child, parent := range parents {
		if parent != nil {
			children[*parent] = append(children[*parent], child)
		}
	}
	var height func(n uuid.UUID, steps int) int
	height = func(n uuid.UUID, steps int) int {
		if steps > len(parents) {
			return steps
		}
		deepest := 0
		for _, c := range children[n] {
			if h := height(c, steps+1); h > deepest {
				deepest = h
			}
		}
		return deepest + 1
	}
	return height(id, 0)
}

// MoveFolder moves a folder, and everything in it, under parentID, or to
// the top level when parentID is nil. It refuses a move into the folder
// itself or anything beneath it, and one that would nest any part of the
// subtree past MaxFolderDepth.
func MoveFolder(ctx context.Context, s tenant.Scope, id uuid.UUID, parentID *uuid.UUID) (*Folder, error) {
	var out Folder
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		if err := lockFolderTree(ctx, tx, s.OrgID()); err != nil {
			return err
		}
		parents, err := loadFolderParents(ctx, tx)
		if err != nil {
			return err
		}
		if _, ok := parents[id]; !ok {
			return ErrNotFound
		}
		parentDepth := 0
		if parentID != nil {
			if _, ok := parents[*parentID]; !ok {
				return ErrNotFound
			}
			if isFolderWithin(parents, *parentID, id) {
				return ErrFolderCycle
			}
			parentDepth = folderDepth(parents, *parentID)
		}
		if parentDepth+folderSubtreeHeight(parents, id) > MaxFolderDepth {
			return ErrFolderTooDeep
		}
		return tx.GetContext(ctx, &out,
			`UPDATE folders SET parent_id = $2 WHERE id = $1 RETURNING `+folderCols, id, parentID)
	})
	if err != nil {
		return nil, folderWriteErr(err)
	}
	return &out, nil
}

// DeleteFolder removes an empty folder and returns it. Anything inside —
// a subfolder, or a project, archived or not — refuses with a
// *FolderNotEmptyError naming what is left. There is no cascade: projects
// are never deleted or silently moved as a side effect of tidying folders.
func DeleteFolder(ctx context.Context, s tenant.Scope, id uuid.UUID) (*Folder, error) {
	var out Folder
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		if err := lockFolderTree(ctx, tx, s.OrgID()); err != nil {
			return err
		}
		if err := tx.GetContext(ctx, &out, `SELECT `+folderCols+` FROM folders WHERE id = $1`, id); err != nil {
			return err
		}
		notEmpty := &FolderNotEmptyError{Name: out.Name}
		if err := tx.GetContext(ctx, &notEmpty.Subfolders,
			`SELECT count(*) FROM folders WHERE parent_id = $1`, id); err != nil {
			return err
		}
		var filed []struct {
			Name     string `db:"name"`
			Archived bool   `db:"archived"`
		}
		if err := tx.SelectContext(ctx, &filed, `
			SELECT name, archived_at IS NOT NULL AS archived
			FROM projects WHERE folder_id = $1 ORDER BY lower(name)`, id); err != nil {
			return err
		}
		for _, p := range filed {
			notEmpty.Projects = append(notEmpty.Projects, p.Name)
			if p.Archived {
				notEmpty.Archived++
			}
		}
		if notEmpty.Subfolders > 0 || len(notEmpty.Projects) > 0 {
			return notEmpty
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM folders WHERE id = $1`, id)
		return err
	})
	if err != nil {
		var notEmpty *FolderNotEmptyError
		if errors.As(err, &notEmpty) {
			return nil, notEmpty
		}
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23503" {
			// A project was filed here between the check and the delete.
			return nil, &FolderNotEmptyError{Name: out.Name}
		}
		return nil, folderWriteErr(err)
	}
	return &out, nil
}
```

- [ ] **Step 4: Run the tests and confirm they pass**

Run: `go test ./internal/repo -run 'Folder|MoveProject|CreateProject_Can|Archiving|DeletingAnOrganization' -v -count=1`
Expected: PASS, including `TestMoveFolder_ConcurrentOppositeMovesCannotFormALoop` across all 20 rounds.

- [ ] **Step 5: Commit**

```bash
git add internal/repo/folders.go internal/repo/folders_test.go
git commit -m "feat(repo): move and delete folders without cycles, overdepth or orphans"
```

---

### Task 4: Repo — the folder rollup

**Files:**
- Create: `internal/repo/folder_rollup.go`
- Test: `internal/repo/folder_rollup_test.go`

**Interfaces:**
- Consumes: `Folder` (Task 2); the existing `ProjectRollup`, `KindRollup` and `collector.Status`.
- Produces:
  - `type PassShare struct{ Passing, Failing, Unknown int }` with `Measured() int` and `Percent() (int, bool)` (rounded half up, `false` when nothing is measured)
  - `type TreeProject struct{ ID uuid.UUID; Name string; FolderID *uuid.UUID; Components int; Rollup ProjectRollup }`
  - `type FailingProject struct{ ID uuid.UUID; Name string; Score *int }`
  - `type FolderRollup struct{ Projects, Components int; Share PassShare; Kinds map[string]PassShare; Failing []FailingProject }`
  - `func RollupFolders(folders []Folder, projects []TreeProject) map[uuid.UUID]FolderRollup` — one entry for every folder, empty ones included

- [ ] **Step 1: Write the failing tests**

Create `internal/repo/folder_rollup_test.go`. It reuses `score` from `project_rollup_test.go`.

```go
package repo_test

import (
	"testing"

	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/collector"
	"github.com/uyab/lotus/internal/repo"
)

func testFolder(name string, parent *repo.Folder) repo.Folder {
	f := repo.Folder{ID: uuid.New(), Name: name}
	if parent != nil {
		pid := parent.ID
		f.ParentID = &pid
	}
	return f
}

// treeProject is a project whose Lotus status and score are already
// rolled up, with its SonarQube kind in the same state.
func treeProject(name string, in *repo.Folder, status collector.Status, s *int) repo.TreeProject {
	p := repo.TreeProject{
		ID: uuid.New(), Name: name, Components: 1,
		Rollup: repo.ProjectRollup{
			Status: status, Score: s,
			Kinds: map[string]repo.KindRollup{"sonarqube": {Status: status, Score: s, Count: 1}},
		},
	}
	if in != nil {
		fid := in.ID
		p.FolderID = &fid
	}
	return p
}

func TestRollupFolders_CountsProjectsAtAnyDepth(t *testing.T) {
	it := testFolder("IT Services", nil)
	tribeA := testFolder("Tribe lead A", &it)
	tribeB := testFolder("Tribe lead B", &it)
	got := repo.RollupFolders(
		[]repo.Folder{it, tribeA, tribeB},
		[]repo.TreeProject{
			treeProject("Linknet", &tribeA, collector.StatusFail, score(42)),
			treeProject("Dignas", &tribeA, collector.StatusPass, score(95)),
			treeProject("ANRI", &tribeB, collector.StatusFail, score(55)),
			treeProject("Unfiled", nil, collector.StatusPass, score(90)),
		})

	if s := got[it.ID].Share; s != (repo.PassShare{Passing: 1, Failing: 2}) {
		t.Fatalf("IT Services share = %+v, want 1 passing 2 failing", s)
	}
	if got[it.ID].Projects != 3 || got[it.ID].Components != 3 {
		t.Fatalf("IT Services counts = %d projects %d components, want 3 and 3", got[it.ID].Projects, got[it.ID].Components)
	}
	if s := got[tribeA.ID].Share; s != (repo.PassShare{Passing: 1, Failing: 1}) {
		t.Fatalf("Tribe lead A share = %+v, want 1 passing 1 failing", s)
	}
	if pct, ok := got[it.ID].Share.Percent(); !ok || pct != 33 {
		t.Fatalf("IT Services percent = %d (%v), want 33", pct, ok)
	}
}

func TestRollupFolders_UnknownIsCountedButNotMeasured(t *testing.T) {
	f := testFolder("Product", nil)
	got := repo.RollupFolders([]repo.Folder{f}, []repo.TreeProject{
		treeProject("AlurKerja", &f, collector.StatusPass, score(84)),
		treeProject("New thing", &f, collector.StatusUnknown, nil),
	})
	s := got[f.ID].Share
	if s.Passing != 1 || s.Unknown != 1 || s.Measured() != 1 {
		t.Fatalf("share = %+v (measured %d), want 1 passing, 1 unknown, 1 measured", s, s.Measured())
	}
	if pct, ok := s.Percent(); !ok || pct != 100 {
		t.Fatalf("percent = %d (%v), want 100", pct, ok)
	}
}

func TestRollupFolders_EmptyFolderIsPresentWithNothingMeasured(t *testing.T) {
	f := testFolder("Empty", nil)
	got := repo.RollupFolders([]repo.Folder{f}, nil)
	r, ok := got[f.ID]
	if !ok {
		t.Fatal("empty folder missing from the rollup")
	}
	if _, measured := r.Share.Percent(); measured || r.Projects != 0 {
		t.Fatalf("empty folder = %+v, want nothing measured", r)
	}
}

func TestRollupFolders_FailingWorstFirstUnscoredLast(t *testing.T) {
	f := testFolder("F", nil)
	got := repo.RollupFolders([]repo.Folder{f}, []repo.TreeProject{
		treeProject("Mid", &f, collector.StatusFail, score(55)),
		treeProject("NoScore", &f, collector.StatusFail, nil),
		treeProject("Worst", &f, collector.StatusFail, score(42)),
		treeProject("Fine", &f, collector.StatusPass, score(99)),
	})
	var names []string
	for _, p := range got[f.ID].Failing {
		names = append(names, p.Name)
	}
	want := []string{"Worst", "Mid", "NoScore"}
	if len(names) != len(want) {
		t.Fatalf("failing = %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("failing = %v, want %v", names, want)
		}
	}
}

func TestRollupFolders_KindSharesSkipProjectsWithoutThatKind(t *testing.T) {
	f := testFolder("F", nil)
	noSonar := treeProject("NoSonar", &f, collector.StatusPass, score(90))
	noSonar.Rollup.Kinds = map[string]repo.KindRollup{"gitlab": {Status: collector.StatusPass, Score: score(90), Count: 1}}
	got := repo.RollupFolders([]repo.Folder{f}, []repo.TreeProject{
		treeProject("A", &f, collector.StatusPass, score(90)),
		treeProject("B", &f, collector.StatusFail, score(40)),
		noSonar,
	})
	if s := got[f.ID].Kinds["sonarqube"]; s != (repo.PassShare{Passing: 1, Failing: 1}) {
		t.Fatalf("sonarqube share = %+v, want 1 passing 1 failing", s)
	}
	if s := got[f.ID].Kinds["gitlab"]; s != (repo.PassShare{Passing: 1}) {
		t.Fatalf("gitlab share = %+v, want 1 passing", s)
	}
}

func TestPassShare_PercentRoundsHalfUp(t *testing.T) {
	cases := []struct {
		share repo.PassShare
		want  int
	}{
		{repo.PassShare{Passing: 4, Failing: 1}, 80},
		{repo.PassShare{Passing: 3, Failing: 1}, 75},
		{repo.PassShare{Passing: 1, Failing: 2}, 33},
		{repo.PassShare{Passing: 2, Failing: 1}, 67},
	}
	for _, c := range cases {
		if got, ok := c.share.Percent(); !ok || got != c.want {
			t.Errorf("%+v percent = %d (%v), want %d", c.share, got, ok, c.want)
		}
	}
}
```

- [ ] **Step 2: Run the tests and confirm they fail to compile**

Run: `go test ./internal/repo -run 'RollupFolders|PassShare' -v`
Expected: FAIL (build error): `undefined: repo.RollupFolders`.

- [ ] **Step 3: Implement the rollup**

Create `internal/repo/folder_rollup.go`:

```go
package repo

import (
	"sort"
	"strings"

	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/collector"
)

// PassShare counts projects by their Lotus status. A folder reports
// "Passing / Measured passing": an unknown project is left out of the
// share and reported beside it instead, so a project nobody has bound yet
// neither drags a folder down nor, by never being measured, props it up.
type PassShare struct {
	Passing, Failing, Unknown int
}

// Measured is how many projects have a verdict.
func (p PassShare) Measured() int { return p.Passing + p.Failing }

// Percent is the share passing, rounded half up — the same rounding the
// KPI cards use. ok is false when nothing is measured.
func (p PassShare) Percent() (int, bool) {
	m := p.Measured()
	if m == 0 {
		return 0, false
	}
	return int(float64(p.Passing)/float64(m)*100 + 0.5), true
}

func (p *PassShare) add(s collector.Status) {
	switch s {
	case collector.StatusPass:
		p.Passing++
	case collector.StatusFail:
		p.Failing++
	default:
		p.Unknown++
	}
}

// TreeProject is one project as the folder rollup sees it: where it is
// filed, and its already-computed Lotus rollup.
type TreeProject struct {
	ID         uuid.UUID
	Name       string
	FolderID   *uuid.UUID
	Components int
	Rollup     ProjectRollup
}

// FailingProject names a failing project for a folder's row.
type FailingProject struct {
	ID    uuid.UUID
	Name  string
	Score *int
}

// FolderRollup is a folder's aggregate over every project beneath it, at
// any depth.
type FolderRollup struct {
	Projects   int
	Components int
	Share      PassShare
	// Kinds is the same share per integration. A project that does not
	// report a kind is not counted in that kind's share at all.
	Kinds map[string]PassShare
	// Failing lists the failing projects, worst score first and unscored
	// ones last — the order to fix them in.
	Failing []FailingProject
}

// RollupFolders aggregates every folder from the projects beneath it.
//
// The number is deliberately a share of projects passing, not an average
// score. An average lets a healthy project hide a failing one and rewards
// polishing an already-green project as much as fixing a red one; a share
// cannot be offset, moves with every fix, and has one target — all green.
// Every project counts once in every folder above it, so a folder never
// averages another folder's number.
func RollupFolders(folders []Folder, projects []TreeProject) map[uuid.UUID]FolderRollup {
	parents := make(map[uuid.UUID]*uuid.UUID, len(folders))
	acc := make(map[uuid.UUID]*FolderRollup, len(folders))
	for _, f := range folders {
		parents[f.ID] = f.ParentID
		acc[f.ID] = &FolderRollup{Kinds: map[string]PassShare{}}
	}

	for _, p := range projects {
		cur := p.FolderID
		for steps := 0; cur != nil && steps <= len(folders); steps++ {
			r, ok := acc[*cur]
			if !ok {
				break
			}
			r.Projects++
			r.Components += p.Components
			r.Share.add(p.Rollup.Status)
			for kind, k := range p.Rollup.Kinds {
				if k.Count == 0 {
					continue
				}
				ks := r.Kinds[kind]
				ks.add(k.Status)
				r.Kinds[kind] = ks
			}
			if p.Rollup.Status == collector.StatusFail {
				r.Failing = append(r.Failing, FailingProject{ID: p.ID, Name: p.Name, Score: p.Rollup.Score})
			}
			cur = parents[*cur]
		}
	}

	out := make(map[uuid.UUID]FolderRollup, len(acc))
	for id, r := range acc {
		sortFailing(r.Failing)
		out[id] = *r
	}
	return out
}

func sortFailing(fs []FailingProject) {
	sort.SliceStable(fs, func(i, j int) bool {
		a, b := fs[i], fs[j]
		if (a.Score == nil) != (b.Score == nil) {
			return b.Score == nil
		}
		if a.Score != nil && *a.Score != *b.Score {
			return *a.Score < *b.Score
		}
		return strings.ToLower(a.Name) < strings.ToLower(b.Name)
	})
}
```

- [ ] **Step 4: Run the tests and confirm they pass**

Run: `go test ./internal/repo -run 'RollupFolders|PassShare' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/repo/folder_rollup.go internal/repo/folder_rollup_test.go
git commit -m "feat(repo): roll folders up as the share of projects passing"
```

---

### Task 5: Web — the tree view model

**Files:**
- Create: `internal/web/foldertree.go`
- Modify: `internal/web/dashboardview.go` (the `dashboardRow` and `dashboardViewModel` structs; replace `buildProjectDashboardViewModel`, currently at lines 405-510)
- Modify: `internal/web/dashboard.go:57-59` (the call site)
- Modify: `internal/web/dashboardview_test.go:237` (the call site)
- Test: `internal/web/foldertree_test.go`

**Interfaces:**
- Consumes: `repo.Folder`, `repo.ListFolders`, `repo.RollupFolders`, `repo.TreeProject`, `repo.PassShare`, `repo.MaxFolderDepth`.
- Produces:
  - `func buildProjectDashboardViewModel(subtitle string, projects []repo.Project, folders []repo.Folder, components []componentInfo, metrics map[uuid.UUID]repo.ComponentMetric) dashboardViewModel`
  - `dashboardRow` gains `IsFolder bool; NodeID, ParentID, Ancestors string; IndentPx int; BarTone string; MoveOptions []httpx.SelectOption; MoveValue, DeleteBlocked string`
  - `dashboardViewModel` gains `Tree bool; FolderOptions []folderOption; FolderSelect []httpx.SelectOption; CanCreateFolder, CanEditFolder, CanDeleteFolder bool`
  - `type folderOption struct{ ID uuid.UUID; Path string; Depth int }`
  - `func folderOptionsFor(folders []repo.Folder) []folderOption`
  - `func folderSelect(opts []folderOption, exclude func(folderOption) bool) []httpx.SelectOption` — "Top level" (value `""`) first
  - `const treeIndentPx = 18`

- [ ] **Step 1: Write the failing tests**

Create `internal/web/foldertree_test.go`. It reuses `intPtr` and `metricsJSON` from `dashboardview_test.go`.

```go
package web

import (
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/repo"
)

// treeFixture builds buildProjectDashboardViewModel's inputs one project at
// a time, so each test reads as the tree it describes.
type treeFixture struct {
	t          *testing.T
	folders    []repo.Folder
	projects   []repo.Project
	components []componentInfo
	metrics    map[uuid.UUID]repo.ComponentMetric
}

func newTreeFixture(t *testing.T) *treeFixture {
	return &treeFixture{t: t, metrics: map[uuid.UUID]repo.ComponentMetric{}}
}

func (f *treeFixture) folder(name string, parent *repo.Folder) repo.Folder {
	fo := repo.Folder{ID: uuid.New(), Name: name}
	if parent != nil {
		pid := parent.ID
		fo.ParentID = &pid
	}
	f.folders = append(f.folders, fo)
	return fo
}

// project adds a project with one component whose SonarQube headline has
// the given status and score. status "" adds a project with no components;
// "unmeasured" adds one whose component has never reported.
func (f *treeFixture) project(name string, in *repo.Folder, status string, score *int) uuid.UUID {
	f.t.Helper()
	p := repo.Project{ID: uuid.New(), Name: name}
	if in != nil {
		fid := in.ID
		p.FolderID = &fid
	}
	f.projects = append(f.projects, p)
	if status == "" {
		return p.ID
	}
	c := componentInfo{ID: uuid.New(), Name: name + "-api", ProjectID: p.ID, ProjectName: name}
	f.components = append(f.components, c)
	if status == "unmeasured" {
		return p.ID
	}
	f.metrics[c.ID] = repo.ComponentMetric{
		ProjectID: p.ID, ComponentID: c.ID, Status: status, Score: score,
		MetricsJSON: metricsJSON(f.t, map[string]repo.KindHeadline{"sonarqube": {Status: status, Score: score}}),
	}
	return p.ID
}

func (f *treeFixture) build() dashboardViewModel {
	return buildProjectDashboardViewModel("", f.projects, f.folders, f.components, f.metrics)
}

func rowNames(vm dashboardViewModel) []string {
	out := make([]string, 0, len(vm.Rows))
	for _, r := range vm.Rows {
		out = append(out, r.Name)
	}
	return out
}

func rowNamed(t *testing.T, vm dashboardViewModel, name string) dashboardRow {
	t.Helper()
	for _, r := range vm.Rows {
		if r.Name == name {
			return r
		}
	}
	t.Fatalf("no row named %q in %v", name, rowNames(vm))
	return dashboardRow{}
}

// projectsOf derives the project list a set of components belongs to, for
// tests written before buildProjectDashboardViewModel took projects.
func projectsOf(comps []componentInfo) []repo.Project {
	seen := map[uuid.UUID]bool{}
	var out []repo.Project
	for _, c := range comps {
		if !seen[c.ProjectID] {
			seen[c.ProjectID] = true
			out = append(out, repo.Project{ID: c.ProjectID, Name: c.ProjectName})
		}
	}
	return out
}

func TestProjectTree_FoldersFirstThenProjectsDepthFirst(t *testing.T) {
	f := newTreeFixture(t)
	product := f.folder("Product", nil)
	it := f.folder("IT Services", nil)
	tribeA := f.folder("Tribe lead A", &it)
	f.project("Unfiled", nil, "pass", intPtr(90))
	f.project("Linknet", &tribeA, "fail", intPtr(42))
	f.project("AlurKerja", &product, "pass", intPtr(84))
	vm := f.build()

	want := []string{"IT Services", "Tribe lead A", "Linknet", "Product", "AlurKerja", "Unfiled"}
	if got := rowNames(vm); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("rows = %v, want %v", got, want)
	}
	if !vm.Tree {
		t.Fatal("Tree = false on the organization dashboard")
	}
	linknet := rowNamed(t, vm, "Linknet")
	if linknet.ParentID != tribeA.ID.String() {
		t.Fatalf("Linknet ParentID = %q, want %q", linknet.ParentID, tribeA.ID)
	}
	if want := it.ID.String() + " " + tribeA.ID.String(); linknet.Ancestors != want {
		t.Fatalf("Linknet Ancestors = %q, want %q", linknet.Ancestors, want)
	}
	if linknet.IndentPx != 2*treeIndentPx {
		t.Fatalf("Linknet IndentPx = %d, want %d", linknet.IndentPx, 2*treeIndentPx)
	}
	if unfiled := rowNamed(t, vm, "Unfiled"); unfiled.ParentID != "" || unfiled.IndentPx != 0 {
		t.Fatalf("Unfiled = parent %q indent %d, want root", unfiled.ParentID, unfiled.IndentPx)
	}
}

func TestProjectTree_FolderRowCountsPassingAtAnyDepthAndNamesFailing(t *testing.T) {
	f := newTreeFixture(t)
	it := f.folder("IT Services", nil)
	tribeA := f.folder("Tribe lead A", &it)
	tribeB := f.folder("Tribe lead B", &it)
	f.project("Linknet", &tribeA, "fail", intPtr(42))
	f.project("Dignas", &tribeA, "pass", intPtr(95))
	f.project("ANRI", &tribeB, "fail", intPtr(55))
	vm := f.build()

	row := rowNamed(t, vm, "IT Services")
	if !row.IsFolder || row.Score != "1 / 3 passing" || row.Tone != "fail" || row.BarTone != "fail" {
		t.Fatalf("IT Services = folder %v, score %q, tone %q, bar %q", row.IsFolder, row.Score, row.Tone, row.BarTone)
	}
	if want := "3 projects · failing: Linknet 42, ANRI 55"; row.Subtext != want {
		t.Fatalf("subtext = %q, want %q", row.Subtext, want)
	}
	if got := rowNamed(t, vm, "Tribe lead A").Score; got != "1 / 2 passing" {
		t.Fatalf("Tribe lead A score = %q, want 1 / 2 passing", got)
	}
}

func TestProjectTree_EightyPercentIsGreenButAnyFailureKeepsTheDotRed(t *testing.T) {
	f := newTreeFixture(t)
	eighty := f.folder("Eighty", nil)
	seventyFive := f.folder("SeventyFive", nil)
	for i := 0; i < 4; i++ {
		f.project("pass80-"+string(rune('a'+i)), &eighty, "pass", intPtr(90))
	}
	f.project("fail80", &eighty, "fail", intPtr(40))
	for i := 0; i < 3; i++ {
		f.project("pass75-"+string(rune('a'+i)), &seventyFive, "pass", intPtr(90))
	}
	f.project("fail75", &seventyFive, "fail", intPtr(40))
	vm := f.build()

	e := rowNamed(t, vm, "Eighty")
	if e.BarTone != "pass" || e.Tone != "fail" || e.ScorePct != "80%" {
		t.Fatalf("80%% folder = bar %q dot %q pct %q, want pass/fail/80%%", e.BarTone, e.Tone, e.ScorePct)
	}
	if s := rowNamed(t, vm, "SeventyFive"); s.BarTone != "fail" {
		t.Fatalf("75%% folder bar = %q, want fail", s.BarTone)
	}
}

func TestProjectTree_EmptyFolderShowsNoVerdict(t *testing.T) {
	f := newTreeFixture(t)
	f.folder("Empty", nil)
	f.project("Somewhere", nil, "pass", intPtr(90))
	row := rowNamed(t, f.build(), "Empty")
	if row.Score != "–" || row.Tone != "none" || row.BarTone != "none" {
		t.Fatalf("empty folder = score %q tone %q bar %q, want –/none/none", row.Score, row.Tone, row.BarTone)
	}
	if row.Subtext != "0 projects · 0 components" {
		t.Fatalf("subtext = %q", row.Subtext)
	}
	if row.DeleteBlocked != "" {
		t.Fatalf("DeleteBlocked = %q on an empty folder, want empty", row.DeleteBlocked)
	}
}

func TestProjectTree_UnmeasuredProjectsAreCalledOutNotCounted(t *testing.T) {
	f := newTreeFixture(t)
	product := f.folder("Product", nil)
	f.project("AlurKerja", &product, "pass", intPtr(84))
	f.project("Never reported", &product, "unmeasured", nil)
	f.project("No components", &product, "", nil)
	vm := f.build()

	row := rowNamed(t, vm, "Product")
	if row.Score != "1 / 1 passing" {
		t.Fatalf("score = %q, want 1 / 1 passing", row.Score)
	}
	if !strings.Contains(row.Subtext, "2 not measured") {
		t.Fatalf("subtext = %q, want it to call out 2 not measured", row.Subtext)
	}
	if nc := rowNamed(t, vm, "No components"); nc.Subtext != "0 components" || nc.Tone != "none" {
		t.Fatalf("componentless project row = %+v, want a 0 components row with no verdict", nc)
	}
	if row.DeleteBlocked == "" {
		t.Fatal("DeleteBlocked is empty on a folder holding projects")
	}
}

func TestProjectTree_FailingNamesCapAtThree(t *testing.T) {
	f := newTreeFixture(t)
	bad := f.folder("Bad", nil)
	for i, name := range []string{"A", "B", "C", "D", "E"} {
		f.project(name, &bad, "fail", intPtr(10*(i+1)))
	}
	row := rowNamed(t, f.build(), "Bad")
	if want := "5 projects · failing: A 10, B 20, C 30 +2 more"; row.Subtext != want {
		t.Fatalf("subtext = %q, want %q", row.Subtext, want)
	}
}

func TestProjectTree_KindCellsFollowBindableKindsAndCountShares(t *testing.T) {
	f := newTreeFixture(t)
	fo := f.folder("F", nil)
	f.project("P1", &fo, "pass", intPtr(90))
	f.project("P2", &fo, "fail", intPtr(40))
	row := rowNamed(t, f.build(), "F")

	if len(row.Cells) != len(projectColumns) {
		t.Fatalf("%d cells, want %d (one per projectColumns entry)", len(row.Cells), len(projectColumns))
	}
	for i, col := range projectColumns {
		want := "–"
		if col.Key == "sonarqube" {
			want = "1/2"
		}
		if row.Cells[i].Text != want {
			t.Fatalf("cell %d (%s) = %q, want %q", i, col.Key, row.Cells[i].Text, want)
		}
	}
}

func TestProjectTree_FilingProjectsDoesNotChangeTheOrgScore(t *testing.T) {
	flat := newTreeFixture(t)
	flat.project("A", nil, "pass", intPtr(90))
	flat.project("B", nil, "fail", intPtr(40))

	filed := newTreeFixture(t)
	fo := filed.folder("F", nil)
	filed.project("A", &fo, "pass", intPtr(90))
	filed.project("B", nil, "fail", intPtr(40))

	a, b := flat.build().Score, filed.build().Score
	if a != b {
		t.Fatalf("org score changed by filing a project: %+v vs %+v", a, b)
	}
}

func TestProjectTree_FoldersWithoutComponentsStillShowTheTree(t *testing.T) {
	f := newTreeFixture(t)
	fo := f.folder("Product", nil)
	f.project("Fresh", &fo, "", nil)
	vm := f.build()
	if vm.Empty {
		t.Fatal("Empty = true for an org that has folders")
	}
	if got := rowNames(vm); len(got) != 2 {
		t.Fatalf("rows = %v, want the folder and its project", got)
	}
}

func TestFolderOptions_PathsAndCreateTargetsSkipMaxDepth(t *testing.T) {
	f := newTreeFixture(t)
	it := f.folder("IT Services", nil)
	f.folder("Tribe lead A", &it)
	var parent *repo.Folder
	for i := 1; i <= repo.MaxFolderDepth; i++ {
		fo := f.folder("L"+string(rune('0'+i)), parent)
		parent = &fo
	}
	f.project("P", nil, "pass", intPtr(90))
	vm := f.build()

	labels := map[string]bool{}
	for _, o := range vm.FolderSelect {
		labels[o.Label] = true
	}
	if vm.FolderSelect[0].Value != "" || vm.FolderSelect[0].Label != "Top level" {
		t.Fatalf("first option = %+v, want Top level", vm.FolderSelect[0])
	}
	if !labels["IT Services / Tribe lead A"] {
		t.Fatalf("options %v lack the nested path", labels)
	}
	if labels["L1 / L2 / L3 / L4 / L5 / L6 / L7 / L8"] {
		t.Fatal("a folder at max depth is offered as a parent for a new folder")
	}
	if !labels["L1 / L2 / L3 / L4 / L5 / L6 / L7"] {
		t.Fatal("a folder one level short of max depth is missing")
	}
}

func TestProjectTree_MoveOptionsExcludeTheFolderItsSubtreeAndTooDeepTargets(t *testing.T) {
	f := newTreeFixture(t)
	it := f.folder("IT Services", nil)
	f.folder("Tribe lead A", &it)
	f.folder("Product", nil)
	var parent *repo.Folder
	for i := 1; i <= 7; i++ {
		fo := f.folder("D"+string(rune('0'+i)), parent)
		parent = &fo
	}
	f.project("P", nil, "pass", intPtr(90))
	vm := f.build()

	offered := map[string]bool{}
	for _, o := range rowNamed(t, vm, "IT Services").MoveOptions {
		offered[o.Label] = true
	}
	if offered["IT Services"] || offered["IT Services / Tribe lead A"] {
		t.Fatalf("IT Services may move into itself: %v", offered)
	}
	if !offered["Top level"] || !offered["Product"] {
		t.Fatalf("valid targets missing: %v", offered)
	}
	// IT Services spans 2 levels: under D6 is 6+2 = 8 (fine), under D7 is 9.
	if !offered["D1 / D2 / D3 / D4 / D5 / D6"] {
		t.Fatal("D6 should be offered")
	}
	if offered["D1 / D2 / D3 / D4 / D5 / D6 / D7"] {
		t.Fatal("D7 would nest past the cap and should not be offered")
	}
	if got := rowNamed(t, vm, "Tribe lead A").MoveValue; got != it.ID.String() {
		t.Fatalf("Tribe lead A MoveValue = %q, want its parent %q", got, it.ID)
	}
}
```

- [ ] **Step 2: Update the existing call site in the tests**

In `internal/web/dashboardview_test.go` (currently line 237), replace

```go
	onDashboard := buildProjectDashboardViewModel("", 1, comps, metrics)
```

with

```go
	onDashboard := buildProjectDashboardViewModel("", projectsOf(comps), nil, comps, metrics)
```

- [ ] **Step 3: Run the tests and confirm they fail to compile**

Run: `go test ./internal/web -run 'ProjectTree|FolderOptions|ProjectScore' -v`
Expected: FAIL (build error): `too many arguments in call to buildProjectDashboardViewModel` and `undefined: treeIndentPx`.

- [ ] **Step 4: Extend the row and view-model structs**

In `internal/web/dashboardview.go`, add to `dashboardRow` after `TrendURL`:

```go

	// The folder tree (organization dashboard only). NodeID is the row's
	// folder or project id; ParentID its enclosing folder ("" at the top);
	// Ancestors every enclosing folder id, outermost first, space-separated
	// — what a collapsed folder hides its descendants by.
	IsFolder  bool
	NodeID    string
	ParentID  string
	Ancestors string
	IndentPx  int

	// BarTone colours the score text and bar. On a project row it is the
	// status. On a folder row it is the share passing against the 80% bar,
	// while Tone stays the status dot — red whenever anything inside fails,
	// so a folder at 80% never looks all-clear.
	BarTone string

	// Folder rows only: the actions dialog's move picker, its current value
	// (the parent's id, "" at the top), and why Delete is unavailable
	// ("" when the folder is empty).
	MoveOptions   []httpx.SelectOption
	MoveValue     string
	DeleteBlocked string
```

Add to `dashboardViewModel` after `Subtitle`:

```go

	// Tree turns on the folder tree: folder rows, collapse, tree-aware
	// sorting and the folder dialogs. Organization dashboard only.
	Tree bool
	// FolderOptions is every folder depth-first with its full path;
	// FolderSelect is the new-folder parent picker built from it.
	FolderOptions []folderOption
	FolderSelect  []httpx.SelectOption
	// The page handler sets these from the principal. The routes enforce
	// the same permissions regardless; these only decide what is drawn.
	CanCreateFolder, CanEditFolder, CanDeleteFolder bool
```

If `internal/web/dashboardview.go` does not import `"github.com/uyab/lotus/internal/httpx"` yet, add it.

- [ ] **Step 5: Replace `buildProjectDashboardViewModel`**

Leave the function's existing doc comment in place and append this paragraph to the end of it:

```go
//
// Every active project gets a row, even one with no components yet: a
// project filed in a folder must be visible, or the folder would look empty
// and still refuse to be deleted. Rows are arranged in the organization's
// folder tree, with a row per folder (arrangeProjectTree, foldertree.go).
```

Then replace everything from the `func buildProjectDashboardViewModel(` line through the function's closing brace (currently lines 417-510) with:

```go
func buildProjectDashboardViewModel(subtitle string, projects []repo.Project, folders []repo.Folder, components []componentInfo, metrics map[uuid.UUID]repo.ComponentMetric) dashboardViewModel {
	if len(components) == 0 && len(folders) == 0 {
		return dashboardViewModel{Empty: true, NoProjects: len(projects) == 0, ProjectCount: len(projects), Subtitle: subtitle}
	}

	componentCount := make(map[uuid.UUID]int, len(projects))
	projectMetrics := make(map[uuid.UUID][]repo.ComponentMetric, len(projects))
	kindStatuses := map[string][]collector.Status{"gitlab": nil, "sonarqube": nil, "trivy": nil, "sentry": nil}
	kindMeasured := map[string]int{}

	for _, comp := range components {
		componentCount[comp.ProjectID]++
		cm, has := metrics[comp.ID]
		if !has {
			continue
		}
		projectMetrics[comp.ProjectID] = append(projectMetrics[comp.ProjectID], cm)

		// The KPI cards stay component-scoped: they answer "how much of the
		// fleet reports this integration at all", which is a coverage
		// question about components, not about projects.
		for kind := range kindStatuses {
			if kh, ok := cm.Metrics()[kind]; ok {
				kindStatuses[kind] = append(kindStatuses[kind], collector.Status(kh.Status))
				kindMeasured[kind]++
			}
		}
	}

	projectRows := make(map[uuid.UUID]dashboardRow, len(projects))
	treeProjects := make([]repo.TreeProject, 0, len(projects))
	var statuses []collector.Status
	var scores []*int
	for _, p := range projects {
		roll := repo.RollupProject(projectMetrics[p.ID])
		statuses = append(statuses, roll.Status)
		scores = append(scores, roll.Score)
		projectRows[p.ID] = projectRow(p, componentCount[p.ID], roll)
		treeProjects = append(treeProjects, repo.TreeProject{
			ID: p.ID, Name: p.Name, FolderID: p.FolderID,
			Components: componentCount[p.ID], Rollup: roll,
		})
	}

	options := folderOptionsFor(folders)
	rows := arrangeProjectTree(projects, folders, options, projectRows, repo.RollupFolders(folders, treeProjects))

	return dashboardViewModel{
		TableTitle:     "Projects",
		RowHeading:     "PROJECT",
		Columns:        projectColumns,
		FirstMetricCol: firstMetricCol(false),
		GridTemplate:   gridTemplateFor(projectColumns, false),
		Tree:           true,
		FolderOptions:  options,
		FolderSelect: folderSelect(options, func(o folderOption) bool {
			return o.Depth >= repo.MaxFolderDepth
		}),
		ProjectCount: len(projects),
		// The org-wide numbers are over projects and components, never
		// folders: filing a project must not move them.
		Score:    buildScoreCard(statuses, repo.RollupScore(scores)),
		KPIs:     buildKPIs(kindStatuses, kindMeasured, len(components)),
		Rows:     rows,
		RowCount: len(rows),
		Subtitle: subtitle,
	}
}
```

- [ ] **Step 6: Create the tree helpers**

Create `internal/web/foldertree.go`:

```go
package web

import (
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/httpx"
	"github.com/uyab/lotus/internal/repo"
)

// treeIndentPx is how far each folder level indents the rows inside it.
const treeIndentPx = 18

// maxFailingNamed caps how many failing projects a folder row names before
// "+N more".
const maxFailingNamed = 3

// folderOption is one folder as a picker lists it.
type folderOption struct {
	ID    uuid.UUID
	Path  string // "IT Services / Tribe lead A"
	Depth int    // 1 at the top level
}

func parentKey(id *uuid.UUID) uuid.UUID {
	if id == nil {
		return uuid.Nil
	}
	return *id
}

func sortFolders(fs []repo.Folder) {
	sort.SliceStable(fs, func(i, j int) bool {
		if fs[i].SortOrder != fs[j].SortOrder {
			return fs[i].SortOrder < fs[j].SortOrder
		}
		return strings.ToLower(fs[i].Name) < strings.ToLower(fs[j].Name)
	})
}

func sortProjects(ps []repo.Project) {
	sort.SliceStable(ps, func(i, j int) bool {
		if ps[i].SortOrder != ps[j].SortOrder {
			return ps[i].SortOrder < ps[j].SortOrder
		}
		return strings.ToLower(ps[i].Name) < strings.ToLower(ps[j].Name)
	})
}

func childFolders(folders []repo.Folder) map[uuid.UUID][]repo.Folder {
	children := make(map[uuid.UUID][]repo.Folder, len(folders))
	for _, f := range folders {
		k := parentKey(f.ParentID)
		children[k] = append(children[k], f)
	}
	for k := range children {
		sortFolders(children[k])
	}
	return children
}

// folderOptionsFor lists every folder depth-first, each with its full path.
func folderOptionsFor(folders []repo.Folder) []folderOption {
	children := childFolders(folders)
	out := make([]folderOption, 0, len(folders))
	seen := make(map[uuid.UUID]bool, len(folders))
	var walk func(parent uuid.UUID, path string, depth int)
	walk = func(parent uuid.UUID, path string, depth int) {
		for _, f := range children[parent] {
			if seen[f.ID] {
				continue
			}
			seen[f.ID] = true
			p := f.Name
			if path != "" {
				p = path + " / " + f.Name
			}
			out = append(out, folderOption{ID: f.ID, Path: p, Depth: depth})
			walk(f.ID, p, depth+1)
		}
	}
	walk(uuid.Nil, "", 1)
	return out
}

// folderSelect turns folder options into a picker: "Top level" first, then
// every folder by path, minus any the caller excludes.
func folderSelect(opts []folderOption, exclude func(folderOption) bool) []httpx.SelectOption {
	out := make([]httpx.SelectOption, 0, len(opts)+1)
	out = append(out, httpx.SelectOption{Value: "", Label: "Top level"})
	for _, o := range opts {
		if exclude != nil && exclude(o) {
			continue
		}
		out = append(out, httpx.SelectOption{Value: o.ID.String(), Label: o.Path})
	}
	return out
}

// folderSubtree returns a folder and everything beneath it, and how many
// levels that subtree spans (1 for a folder with no subfolders).
func folderSubtree(children map[uuid.UUID][]repo.Folder, id uuid.UUID) (map[uuid.UUID]bool, int) {
	members := map[uuid.UUID]bool{}
	var visit func(n uuid.UUID) int
	visit = func(n uuid.UUID) int {
		if members[n] {
			return 0
		}
		members[n] = true
		deepest := 0
		for _, c := range children[n] {
			if h := visit(c.ID); h > deepest {
				deepest = h
			}
		}
		return deepest + 1
	}
	height := visit(id)
	return members, height
}

func projectRow(p repo.Project, components int, roll repo.ProjectRollup) dashboardRow {
	scoreStr, scorePct := "–", "0%"
	if roll.Score != nil {
		scoreStr = fmt.Sprintf("%d", *roll.Score)
		scorePct = fmt.Sprintf("%d%%", clamp(*roll.Score, 0, 100))
	}
	tone := toneOf(roll.Status)
	return dashboardRow{
		Name:       p.Name,
		Subtext:    fmt.Sprintf("%d component%s", components, plural(components, "", "s")),
		Href:       "/projects/" + p.ID.String(),
		Score:      scoreStr,
		ScoreValue: roll.Score,
		ScorePct:   scorePct,
		Tone:       tone,
		BarTone:    tone,
		NodeID:     p.ID.String(),
		Cells:      cellsFor(projectColumns, func(k string) badgeCell { return projectBadge(roll, k) }),
	}
}

func folderRow(f repo.Folder, r repo.FolderRollup) dashboardRow {
	row := dashboardRow{
		IsFolder: true, Name: f.Name, NodeID: f.ID.String(), Subtext: folderSubtext(r),
		Score: "–", ScorePct: "0%", Tone: "none", BarTone: "none",
	}
	if pct, ok := r.Share.Percent(); ok {
		row.Score = fmt.Sprintf("%d / %d passing", r.Share.Passing, r.Share.Measured())
		row.ScoreValue = &pct
		row.ScorePct = fmt.Sprintf("%d%%", pct)
		row.BarTone = rateTone(pct)
		row.Tone = "pass"
		if r.Share.Failing > 0 {
			row.Tone = "fail"
		}
	}
	row.Cells = cellsFor(projectColumns, func(k string) badgeCell { return folderBadge(r.Kinds[k]) })
	return row
}

// folderBadge shows one integration's share across a folder's projects:
// "2/3" means 2 of the 3 projects reporting it pass it.
func folderBadge(ps repo.PassShare) badgeCell {
	pct, ok := ps.Percent()
	if !ok {
		return badgeCell{Text: "–", Tone: "none"}
	}
	return badgeCell{
		Text:  fmt.Sprintf("%d/%d", ps.Passing, ps.Measured()),
		Tone:  rateTone(pct),
		Title: fmt.Sprintf("%d of %d projects reporting this pass it", ps.Passing, ps.Measured()),
		Sort:  &pct,
	}
}

// folderSubtext names what a team should fix next rather than restating
// the number: the failing projects, worst first, when there are any.
func folderSubtext(r repo.FolderRollup) string {
	parts := []string{fmt.Sprintf("%d project%s", r.Projects, plural(r.Projects, "", "s"))}
	if n := len(r.Failing); n > 0 {
		names := make([]string, 0, maxFailingNamed)
		for _, p := range r.Failing[:min(n, maxFailingNamed)] {
			if p.Score != nil {
				names = append(names, fmt.Sprintf("%s %d", p.Name, *p.Score))
			} else {
				names = append(names, p.Name)
			}
		}
		s := "failing: " + strings.Join(names, ", ")
		if n > maxFailingNamed {
			s += fmt.Sprintf(" +%d more", n-maxFailingNamed)
		}
		parts = append(parts, s)
	} else {
		parts = append(parts, fmt.Sprintf("%d component%s", r.Components, plural(r.Components, "", "s")))
	}
	if r.Share.Unknown > 0 {
		parts = append(parts, fmt.Sprintf("%d not measured", r.Share.Unknown))
	}
	return strings.Join(parts, " · ")
}

func folderDeleteBlocked(projects, subfolders int) string {
	var parts []string
	if subfolders > 0 {
		parts = append(parts, fmt.Sprintf("%d folder%s", subfolders, plural(subfolders, "", "s")))
	}
	if projects > 0 {
		parts = append(parts, fmt.Sprintf("%d project%s", projects, plural(projects, "", "s")))
	}
	if len(parts) == 0 {
		return ""
	}
	return "Still holds " + strings.Join(parts, " and ") + " · move them out first"
}

// arrangeProjectTree lays the rows out depth-first: in each folder, its
// subfolders (each followed by its own contents), then its projects. Root
// projects come after the top-level folders.
func arrangeProjectTree(projects []repo.Project, folders []repo.Folder, options []folderOption,
	projectRows map[uuid.UUID]dashboardRow, rollups map[uuid.UUID]repo.FolderRollup) []dashboardRow {

	children := childFolders(folders)
	known := make(map[uuid.UUID]bool, len(folders))
	for _, f := range folders {
		known[f.ID] = true
	}
	filed := make(map[uuid.UUID][]repo.Project, len(folders)+1)
	for _, p := range projects {
		k := parentKey(p.FolderID)
		if !known[k] {
			// Filed in a folder this page did not load: show it at the root
			// rather than let it vanish.
			k = uuid.Nil
		}
		filed[k] = append(filed[k], p)
	}
	for k := range filed {
		sortProjects(filed[k])
	}

	rows := make([]dashboardRow, 0, len(projects)+len(folders))
	seen := make(map[uuid.UUID]bool, len(folders))
	var walk func(parent uuid.UUID, depth int, ancestors []string)
	walk = func(parent uuid.UUID, depth int, ancestors []string) {
		parentID := ""
		if parent != uuid.Nil {
			parentID = parent.String()
		}
		place := func(row dashboardRow) dashboardRow {
			row.ParentID = parentID
			row.Ancestors = strings.Join(ancestors, " ")
			row.IndentPx = depth * treeIndentPx
			return row
		}
		for _, f := range children[parent] {
			if seen[f.ID] {
				continue
			}
			seen[f.ID] = true
			r := rollups[f.ID]
			row := place(folderRow(f, r))
			members, height := folderSubtree(children, f.ID)
			row.MoveValue = parentID
			row.MoveOptions = folderSelect(options, func(o folderOption) bool {
				return members[o.ID] || o.Depth+height > repo.MaxFolderDepth
			})
			row.DeleteBlocked = folderDeleteBlocked(r.Projects, len(children[f.ID]))
			rows = append(rows, row)
			walk(f.ID, depth+1, append(append([]string(nil), ancestors...), f.ID.String()))
		}
		for _, p := range filed[parent] {
			rows = append(rows, place(projectRows[p.ID]))
		}
	}
	walk(uuid.Nil, 0, nil)

	for i := range rows {
		rows[i].N = i + 1
	}
	return rows
}
```

- [ ] **Step 7: Update the page's call site so it compiles**

In `internal/web/dashboard.go`, directly before `subtitle := …` (currently line 57), load the folders:

```go
		folders, err := repo.ListFolders(c.Context(), s)
		if err != nil {
			return writeRepoErr(c, err)
		}
```

Then replace the builder call:

```go
		vm := buildProjectDashboardViewModel(subtitle, projects, folders, components, metrics)
		p := mustPrincipal(c)
		vm.CanCreateFolder = p.Can("folder.create")
		vm.CanEditFolder = p.Can("folder.update")
		vm.CanDeleteFolder = p.Can("folder.delete")
```

- [ ] **Step 8: Build and run the web tests**

Run: `go build ./... && go vet ./internal/web && go test ./internal/web -run 'ProjectTree|FolderOptions|ProjectScore|BuildDashboard|KindOrder' -v`
Expected: PASS. If `go vet` or the build reports an unused import in `dashboardview.go` (for example `sort`, if the removed alphabetical sort was its last use), delete that import.

- [ ] **Step 9: Commit**

```bash
git add internal/web/foldertree.go internal/web/foldertree_test.go internal/web/dashboardview.go internal/web/dashboardview_test.go internal/web/dashboard.go
git commit -m "feat(web): arrange the projects dashboard as a folder tree"
```

---

### Task 6: Web — folder routes, audit and error copy

**Files:**
- Create: `internal/web/folders.go`
- Create: `web/templates/components/form_error.html`
- Modify: `internal/web/flash.go` (the `flashMessages` map)
- Modify: `internal/web/server.go` (register the routes, next to `registerProjectPageDetailRoutes` at line 134)
- Test: `internal/web/folders_test.go`

**Interfaces:**
- Consumes: `repo.CreateFolder`, `RenameFolder`, `MoveFolder`, `DeleteFolder`, `FolderByID`, `MoveProject`, the error sentinels, `repo.RecordAuditEvent`, `setFlash`, `redirectAfterMutation`, `mustScope`, and `authz.CurrentPrincipal`.
- Produces: routes `POST /folders`, `POST /folders/:id/rename`, `POST /folders/:id/move`, `POST /folders/:id/delete` and `POST /projects/:id/move`; the template `form_error`; `func registerFolderRoutes(app *fiber.App, d Deps, requireSession, loadPrincipal fiber.Handler)`.

Every handler answers a rejected form the way `rejectInviteForm` does. An htmx request gets 200 plus a `form_error` fragment, which htmx swaps into the dialog's error slot (htmx does not swap 4xx responses). A plain post gets `fiber.NewError(status, msg)`. On success the handler sets a flash and calls `redirectAfterMutation`, which reloads the page for htmx.

- [ ] **Step 1: Write the failing tests**

Create `internal/web/folders_test.go`:

```go
package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

func folderFormReq(host, path, raw string, form url.Values, htmx bool) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "http://"+host+path, strings.NewReader(form.Encode()))
	req.Host = host
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if htmx {
		req.Header.Set("HX-Request", "true")
	}
	req.AddCookie(&http.Cookie{Name: "lotus_session", Value: raw})
	return req
}

func folderIDNamed(t *testing.T, db *sqlx.DB, orgID uuid.UUID, name string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	withOrgTx(t, db, orgID, func(tx *sqlx.Tx) {
		if err := tx.Get(&id, `SELECT id FROM folders WHERE name = $1`, name); err != nil {
			t.Fatalf("find folder %q: %v", name, err)
		}
	})
	return id
}

func auditCount(t *testing.T, db *sqlx.DB, orgID uuid.UUID, action string) int {
	t.Helper()
	var n int
	withOrgTx(t, db, orgID, func(tx *sqlx.Tx) {
		if err := tx.Get(&n, `SELECT count(*) FROM audit_events WHERE action = $1`, action); err != nil {
			t.Fatalf("count audit %s: %v", action, err)
		}
	})
	return n
}

func TestFolders_MemberCreatesNestedFoldersAndItIsAudited(t *testing.T) {
	app, db := newDashboardTestApp(t)
	orgID, userID := seedMember(t, db, "acme-fold", "member")
	raw := seedSession(t, db, orgID, userID)
	host := "acme-fold." + dashboardTestDomain

	resp, err := app.Test(folderFormReq(host, "/folders", raw, url.Values{"name": {"IT Services"}}, false))
	if err != nil || resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/projects" {
		t.Fatalf("create = %v, %v; want 302 to /projects", resp, err)
	}
	it := folderIDNamed(t, db, orgID, "IT Services")

	resp, err = app.Test(folderFormReq(host, "/folders", raw,
		url.Values{"name": {"Tribe lead A"}, "parent_id": {it.String()}}, true))
	if err != nil || resp.StatusCode != http.StatusNoContent || resp.Header.Get("HX-Refresh") != "true" {
		t.Fatalf("htmx create = %v, %v; want 204 with HX-Refresh", resp, err)
	}
	folderIDNamed(t, db, orgID, "Tribe lead A")
	if n := auditCount(t, db, orgID, "folder.create"); n != 2 {
		t.Fatalf("folder.create audit events = %d, want 2", n)
	}
}

func TestFolders_ViewerCannotCreate_MemberCannotDelete(t *testing.T) {
	app, db := newDashboardTestApp(t)
	orgID, viewerID := seedMember(t, db, "acme-perm", "viewer")
	viewerRaw := seedSession(t, db, orgID, viewerID)
	host := "acme-perm." + dashboardTestDomain

	resp, _ := app.Test(folderFormReq(host, "/folders", viewerRaw, url.Values{"name": {"X"}}, false))
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer create status = %d, want 403", resp.StatusCode)
	}

	var memberID uuid.UUID
	if err := db.Get(&memberID, `INSERT INTO users (email, email_verified_at) VALUES ('m@acme-perm.test', now()) RETURNING id`); err != nil {
		t.Fatalf("insert member: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO memberships (org_id, user_id, role_id) SELECT $1, $2, id FROM roles WHERE key = 'member' AND org_id IS NULL`, orgID, memberID); err != nil {
		t.Fatalf("membership: %v", err)
	}
	memberRaw := seedSession(t, db, orgID, memberID)
	app.Test(folderFormReq(host, "/folders", memberRaw, url.Values{"name": {"Old"}}, false))
	id := folderIDNamed(t, db, orgID, "Old")

	resp, _ = app.Test(folderFormReq(host, "/folders/"+id.String()+"/delete", memberRaw, nil, false))
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("member delete status = %d, want 403", resp.StatusCode)
	}
}

func TestFolders_RejectionsComeBackIntoTheDialogForHtmx(t *testing.T) {
	app, db := newDashboardTestApp(t)
	orgID, userID := seedMember(t, db, "acme-rej", "owner")
	raw := seedSession(t, db, orgID, userID)
	host := "acme-rej." + dashboardTestDomain
	app.Test(folderFormReq(host, "/folders", raw, url.Values{"name": {"Product"}}, false))
	product := folderIDNamed(t, db, orgID, "Product")
	app.Test(folderFormReq(host, "/folders", raw, url.Values{"name": {"Inner"}, "parent_id": {product.String()}}, false))
	inner := folderIDNamed(t, db, orgID, "Inner")

	cases := []struct {
		name, path string
		form       url.Values
		wantStatus int
		wantText   string
	}{
		{"duplicate name", "/folders", url.Values{"name": {"product"}}, http.StatusConflict,
			"There's already a folder called 'product' here."},
		{"blank name", "/folders", url.Values{"name": {"  "}}, http.StatusUnprocessableEntity,
			"Folder name is required"},
		{"cycle", "/folders/" + product.String() + "/move", url.Values{"parent_id": {inner.String()}}, http.StatusConflict,
			"Can't move a folder into itself."},
		{"not empty", "/folders/" + product.String() + "/delete", nil, http.StatusConflict,
			"'Product' still contains 1 folder."},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resp, err := app.Test(folderFormReq(host, c.path, raw, c.form, true))
			if err != nil || resp.StatusCode != http.StatusOK {
				t.Fatalf("htmx status = %v, %v; want 200", resp, err)
			}
			body, _ := io.ReadAll(resp.Body)
			if !strings.Contains(string(body), c.wantText) && !strings.Contains(string(body), strings.ReplaceAll(c.wantText, "'", "&#39;")) {
				t.Fatalf("htmx body = %s, want it to contain %q", body, c.wantText)
			}
			resp, _ = app.Test(folderFormReq(host, c.path, raw, c.form, false))
			if resp.StatusCode != c.wantStatus {
				t.Fatalf("plain status = %d, want %d", resp.StatusCode, c.wantStatus)
			}
		})
	}
}

func TestFolders_RenameMoveDeleteAreAudited(t *testing.T) {
	app, db := newDashboardTestApp(t)
	orgID, userID := seedMember(t, db, "acme-aud", "admin")
	raw := seedSession(t, db, orgID, userID)
	host := "acme-aud." + dashboardTestDomain
	app.Test(folderFormReq(host, "/folders", raw, url.Values{"name": {"IT"}}, false))
	app.Test(folderFormReq(host, "/folders", raw, url.Values{"name": {"Tribe"}}, false))
	it := folderIDNamed(t, db, orgID, "IT")
	tribe := folderIDNamed(t, db, orgID, "Tribe")

	for _, step := range []struct{ path string; form url.Values }{
		{"/folders/" + it.String() + "/rename", url.Values{"name": {"IT Services"}}},
		{"/folders/" + tribe.String() + "/move", url.Values{"parent_id": {it.String()}}},
		{"/folders/" + tribe.String() + "/move", url.Values{"parent_id": {""}}},
		{"/folders/" + tribe.String() + "/delete", nil},
	} {
		resp, err := app.Test(folderFormReq(host, step.path, raw, step.form, false))
		if err != nil || resp.StatusCode != http.StatusFound {
			t.Fatalf("%s = %v, %v; want 302", step.path, resp, err)
		}
	}
	for action, want := range map[string]int{"folder.rename": 1, "folder.move": 2, "folder.delete": 1} {
		if n := auditCount(t, db, orgID, action); n != want {
			t.Fatalf("%s audit events = %d, want %d", action, n, want)
		}
	}
}

func TestProjects_MoveIntoAFolderIsAudited(t *testing.T) {
	app, db := newDashboardTestApp(t)
	orgID, userID := seedMember(t, db, "acme-mv", "member")
	raw := seedSession(t, db, orgID, userID)
	host := "acme-mv." + dashboardTestDomain
	app.Test(folderFormReq(host, "/folders", raw, url.Values{"name": {"Product"}}, false))
	product := folderIDNamed(t, db, orgID, "Product")
	var projectID uuid.UUID
	withOrgTx(t, db, orgID, func(tx *sqlx.Tx) {
		if err := tx.Get(&projectID, `INSERT INTO projects (org_id, slug, name) VALUES ($1,'syarq','SyarQ') RETURNING id`, orgID); err != nil {
			t.Fatalf("insert project: %v", err)
		}
	})

	resp, err := app.Test(folderFormReq(host, "/projects/"+projectID.String()+"/move", raw,
		url.Values{"folder_id": {product.String()}}, false))
	if err != nil || resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/projects/"+projectID.String() {
		t.Fatalf("move = %v, %v; want 302 back to the project", resp, err)
	}
	var folderID *uuid.UUID
	withOrgTx(t, db, orgID, func(tx *sqlx.Tx) {
		if err := tx.Get(&folderID, `SELECT folder_id FROM projects WHERE id = $1`, projectID); err != nil {
			t.Fatalf("read project: %v", err)
		}
	})
	if folderID == nil || *folderID != product {
		t.Fatalf("folder_id = %v, want %v", folderID, product)
	}
	if n := auditCount(t, db, orgID, "project.move"); n != 1 {
		t.Fatalf("project.move audit events = %d, want 1", n)
	}
}
```

- [ ] **Step 2: Run the tests and confirm they fail**

Run: `go test ./internal/web -run 'TestFolders_|TestProjects_Move' -v`
Expected: FAIL. The folder routes are not registered, so requests return 404 or 405 (for example `create = &{404 …}; want 302`).

- [ ] **Step 3: Add the error fragment template**

Create `web/templates/components/form_error.html`:

```html
{{/* form_error is the alert a dialog shows when its form was rejected. It
     is returned on its own as an htmx fragment and swapped into the
     dialog's error slot, so the dialog stays open with what was typed.
     Takes "." = the message. */}}
{{define "form_error"}}
<div role="alert" style="display:flex;align-items:center;gap:9px;padding:9px 11px;border-radius:var(--radius-petal-sm);background:var(--status-fail-bg);color:var(--status-fail);font-size:12.5px">
  <i class="lotus-status-dot lotus-status-dot-fail" aria-hidden="true"></i>
  <span>{{.}}</span>
</div>
{{end}}
```

- [ ] **Step 4: Add the flash codes**

In `internal/web/flash.go`, add to `flashMessages`:

```go
	"folder.created":     {"pass", "Folder %s created"},
	"folder.renamed":     {"pass", "Folder renamed to %s"},
	"folder.moved":       {"pass", "Folder %s moved"},
	"folder.deleted":     {"pass", "Folder %s deleted"},
	"project.moved":      {"pass", "Project moved"},
```

- [ ] **Step 5: Implement the handlers**

Create `internal/web/folders.go`:

```go
package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/authz"
	"github.com/uyab/lotus/internal/repo"
	"github.com/uyab/lotus/internal/tenant"
)

// Folder routes are form posts, like every other screen action in the app:
// a browser form cannot PATCH or DELETE. Each one answers a rejection the
// way rejectInviteForm does — htmx gets the message inside the still-open
// dialog, a plain post gets the status — and a success with a flash and
// redirectAfterMutation.

// folderFormError answers a rejected folder form.
func folderFormError(c *fiber.Ctx, d Deps, status int, msg string) error {
	if c.Get("HX-Request") == "" {
		return fiber.NewError(status, msg)
	}
	c.Type("html")
	return d.Renderer.RenderNamed(c, "form_error", msg)
}

// folderErr turns a repo error into the status and sentence a person sees.
// name is the folder name a name clash should be reported with.
func folderErr(err error, name string) (int, string, bool) {
	var notEmpty *repo.FolderNotEmptyError
	switch {
	case errors.As(err, &notEmpty):
		return fiber.StatusConflict, notEmpty.Error(), true
	case errors.Is(err, repo.ErrFolderCycle):
		return fiber.StatusConflict, "Can't move a folder into itself.", true
	case errors.Is(err, repo.ErrFolderTooDeep):
		return fiber.StatusConflict, fmt.Sprintf("Folders can nest at most %d levels.", repo.MaxFolderDepth), true
	case errors.Is(err, repo.ErrFolderNameTaken):
		return fiber.StatusConflict, fmt.Sprintf("There's already a folder called '%s' here.", name), true
	case errors.Is(err, repo.ErrInvalidInput):
		return fiber.StatusUnprocessableEntity,
			fmt.Sprintf("Folder name is required and can be at most %d characters.", repo.MaxFolderNameLength), true
	case errors.Is(err, repo.ErrNotFound):
		return fiber.StatusNotFound, "That folder no longer exists.", true
	}
	return 0, "", false
}

// optionalUUID reads a picker value: "" is the top level (nil).
func optionalUUID(v string) (*uuid.UUID, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil, nil
	}
	id, err := uuid.Parse(v)
	if err != nil {
		return nil, err
	}
	return &id, nil
}

func uuidString(id *uuid.UUID) string {
	if id == nil {
		return ""
	}
	return id.String()
}

func folderAuditActor(c *fiber.Ctx) *uuid.UUID {
	if p, ok := authz.CurrentPrincipal(c); ok && p.UserID != uuid.Nil {
		id := p.UserID
		return &id
	}
	return nil
}

func auditFolder(c *fiber.Ctx, s tenant.Scope, action string, id uuid.UUID, meta fiber.Map) error {
	raw, _ := json.Marshal(meta)
	return repo.RecordAuditEvent(c.Context(), s, folderAuditActor(c), action, "folder", &id, raw)
}

func createFolderUI(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, err := mustScope(c)
		if err != nil {
			return err
		}
		name := c.FormValue("name")
		parentID, err := optionalUUID(c.FormValue("parent_id"))
		if err != nil {
			return folderFormError(c, d, fiber.StatusNotFound, "That folder no longer exists.")
		}
		f, err := repo.CreateFolder(c.Context(), s, name, parentID)
		if err != nil {
			if status, msg, ok := folderErr(err, strings.TrimSpace(name)); ok {
				return folderFormError(c, d, status, msg)
			}
			return err
		}
		if err := auditFolder(c, s, "folder.create", f.ID, fiber.Map{"name": f.Name, "parent_id": uuidString(f.ParentID)}); err != nil {
			return writeRepoErr(c, err)
		}
		setFlash(c, "folder.created", f.Name)
		return redirectAfterMutation(c, "/projects")
	}
}

func renameFolderUI(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, err := mustScope(c)
		if err != nil {
			return err
		}
		id, err := uuid.Parse(c.Params("id"))
		if err != nil {
			return fiber.ErrNotFound
		}
		before, err := repo.FolderByID(c.Context(), s, id)
		if err != nil {
			return writeRepoErr(c, err)
		}
		name := c.FormValue("name")
		f, err := repo.RenameFolder(c.Context(), s, id, name)
		if err != nil {
			if status, msg, ok := folderErr(err, strings.TrimSpace(name)); ok {
				return folderFormError(c, d, status, msg)
			}
			return err
		}
		if err := auditFolder(c, s, "folder.rename", f.ID, fiber.Map{"from": before.Name, "to": f.Name}); err != nil {
			return writeRepoErr(c, err)
		}
		setFlash(c, "folder.renamed", f.Name)
		return redirectAfterMutation(c, "/projects")
	}
}

func moveFolderUI(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, err := mustScope(c)
		if err != nil {
			return err
		}
		id, err := uuid.Parse(c.Params("id"))
		if err != nil {
			return fiber.ErrNotFound
		}
		before, err := repo.FolderByID(c.Context(), s, id)
		if err != nil {
			return writeRepoErr(c, err)
		}
		parentID, err := optionalUUID(c.FormValue("parent_id"))
		if err != nil {
			return folderFormError(c, d, fiber.StatusNotFound, "That folder no longer exists.")
		}
		f, err := repo.MoveFolder(c.Context(), s, id, parentID)
		if err != nil {
			if status, msg, ok := folderErr(err, before.Name); ok {
				return folderFormError(c, d, status, msg)
			}
			return err
		}
		if err := auditFolder(c, s, "folder.move", f.ID, fiber.Map{
			"from_parent": uuidString(before.ParentID), "to_parent": uuidString(f.ParentID),
		}); err != nil {
			return writeRepoErr(c, err)
		}
		setFlash(c, "folder.moved", f.Name)
		return redirectAfterMutation(c, "/projects")
	}
}

func deleteFolderUI(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, err := mustScope(c)
		if err != nil {
			return err
		}
		id, err := uuid.Parse(c.Params("id"))
		if err != nil {
			return fiber.ErrNotFound
		}
		f, err := repo.DeleteFolder(c.Context(), s, id)
		if err != nil {
			if status, msg, ok := folderErr(err, ""); ok {
				return folderFormError(c, d, status, msg)
			}
			return err
		}
		if err := auditFolder(c, s, "folder.delete", f.ID, fiber.Map{"name": f.Name}); err != nil {
			return writeRepoErr(c, err)
		}
		setFlash(c, "folder.deleted", f.Name)
		return redirectAfterMutation(c, "/projects")
	}
}

func moveProjectUI(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, err := mustScope(c)
		if err != nil {
			return err
		}
		id, err := uuid.Parse(c.Params("id"))
		if err != nil {
			return fiber.ErrNotFound
		}
		folderID, err := optionalUUID(c.FormValue("folder_id"))
		if err != nil {
			return folderFormError(c, d, fiber.StatusNotFound, "That folder no longer exists.")
		}
		from, err := repo.MoveProject(c.Context(), s, id, folderID)
		if err != nil {
			if errors.Is(err, repo.ErrNotFound) {
				return folderFormError(c, d, fiber.StatusNotFound, "That project or folder no longer exists.")
			}
			return err
		}
		raw, _ := json.Marshal(fiber.Map{"from_folder": uuidString(from), "to_folder": uuidString(folderID)})
		if err := repo.RecordAuditEvent(c.Context(), s, folderAuditActor(c), "project.move", "project", &id, raw); err != nil {
			return writeRepoErr(c, err)
		}
		setFlash(c, "project.moved")
		return redirectAfterMutation(c, "/projects/"+id.String())
	}
}

func registerFolderRoutes(app *fiber.App, d Deps, requireSession, loadPrincipal fiber.Handler) {
	app.Post("/folders", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("folder.create"), createFolderUI(d))
	app.Post("/folders/:id/rename", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("folder.update"), renameFolderUI(d))
	app.Post("/folders/:id/move", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("folder.update"), moveFolderUI(d))
	app.Post("/folders/:id/delete", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("folder.delete"), deleteFolderUI(d))
	app.Post("/projects/:id/move", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("project.update"), moveProjectUI(d))
}
```

- [ ] **Step 6: Register the routes**

In `internal/web/server.go`, directly after the `registerProjectPageDetailRoutes(...)` line (currently 134), add:

```go
	registerFolderRoutes(app, d, authn.RequireTenantSession(authDeps), authz.LoadPrincipal(d.DB))
```

- [ ] **Step 7: Run the tests and confirm they pass**

Run: `go test ./internal/web -run 'TestFolders_|TestProjects_Move|TestRouteCoverage' -v`
Expected: PASS. `TestRouteCoverage` confirms all five new routes carry an `authz.Require` in both saas and single mode.

- [ ] **Step 8: Commit**

```bash
git add internal/web/folders.go internal/web/folders_test.go internal/web/flash.go internal/web/server.go web/templates/components/form_error.html
git commit -m "feat(web): folder create, rename, move and delete routes with audit"
```

---

### Task 7: Web — the projects page tree UI

**Files:**
- Modify: `web/templates/components/dashboard_block.html` (the rows `range` and the section `Attrs`)
- Modify: `web/templates/components/select.html:37` (the `$id` line)
- Create: `web/templates/components/folder_dialogs.html`
- Modify: `web/templates/pages/projects.html`
- Modify: `web/static/js/lotus.js` (`lotusSort.apply`, plus the new `lotusTree`)
- Test: `internal/web/folders_page_test.go`

**Interfaces:**
- Consumes: the tree fields on `dashboardViewModel` and `dashboardRow` (Task 5), and the routes (Task 6).
- Produces: `lotusTree()` in the browser (`filter`, `collapsed`, `toggle(id)`, `isCollapsed(id)`, `hiddenBy(ancestors)`); DOM contract: every tree row has `data-node`, `data-parent` and `data-ancestors`; folder rows also have `data-folder="1"`. Task 9's e2e relies on these.

- [ ] **Step 1: Write the failing page test**

Create `internal/web/folders_page_test.go`:

```go
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
```

- [ ] **Step 2: Run the tests and confirm they fail**

Run: `go test ./internal/web -run TestProjectsPage_ -v`
Expected: FAIL: `page missing "x-data=\"lotusTree()\""`.

- [ ] **Step 3: Give `lotus_select` an optional ID**

Several folder dialogs on one page each carry a `parent_id` picker, and `lotus_select` derives its element id from the field name. Add an optional `ID` key.

In `web/templates/components/select.html`, add a line to the contract comment, after the `"Label"` entry:

```
       "ID"         element id suffix, when two dropdowns on one page
                    submit the same field name — default: Name
```

Replace

```
{{$id := printf "lotus-combobox-%s" .Name}}
```

with

```
{{$id := printf "lotus-combobox-%s" (or .ID .Name)}}
```

- [ ] **Step 4: Render folder rows in `dashboard_block`**

In `web/templates/components/dashboard_block.html`:

1. In the `lotus_card_section_open` call, replace the `Attrs` argument

```
    "Attrs" (printf `x-data="lotusSort('%s')" x-effect="filter; $nextTick(() => renumber())"` .RowHeading))}}
```

with

```
    "Attrs" (printf `x-data="lotusSort('%s')" x-effect="%s $nextTick(() => renumber())"` .RowHeading (ternary .Tree "filter; collapsed;" "filter;")))}}
```

2. Replace the whole `{{range .Rows}} … {{end}}` block inside `<div x-ref="rows" style="display:contents">` with:

```html
    {{range .Rows}}
    {{if .IsFolder}}
    <div class="lotus-repo-row" role="button" tabindex="0" aria-label="Folder {{.Name}}"
         data-node="{{.NodeID}}" data-parent="{{.ParentID}}" data-ancestors="{{.Ancestors}}" data-folder="1"
         style="cursor:pointer"
         @click="toggle($el.dataset.node)" @keydown.enter.prevent="toggle($el.dataset.node)"
         x-show="(filter==='all' || filter==='{{.Tone}}') && !hiddenBy($el.dataset.ancestors)">
      <div style="font:400 10.5px 'IBM Plex Mono',monospace;color:#9CA3AF">{{.N}}</div>
      <div data-sort="{{.Name}}" style="display:flex;align-items:center;gap:8px;min-width:0;padding-left:{{.IndentPx}}px">
        <svg data-node="{{.NodeID}}" width="12" height="12" viewBox="0 0 24 24" fill="none" aria-hidden="true"
             style="flex:none;color:#9CA3AF;transition:transform .12s"
             :style="{ transform: isCollapsed($el.dataset.node) ? 'rotate(-90deg)' : 'rotate(0deg)' }"><path fill="none" stroke="currentColor" stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="m6 9 6 6 6-6"></path></svg>
        {{template "status_dot" .Tone}}
        <svg width="14" height="14" viewBox="0 0 24 24" fill="none" aria-hidden="true" style="flex:none;color:#43284E"><path fill="none" stroke="currentColor" stroke-linejoin="round" stroke-width="1.5" d="M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V7Z"></path></svg>
        <span style="font-size:12.5px;font-weight:700;color:#43284E;overflow:hidden;text-overflow:ellipsis;white-space:nowrap">{{.Name}}</span>
        {{if .Subtext}}<span style="font:400 9.5px 'IBM Plex Mono',monospace;color:#9CA3AF;flex:none;overflow:hidden;text-overflow:ellipsis">{{.Subtext}}</span>{{end}}
        {{if $.CanEditFolder}}
        <button type="button" aria-label="Folder actions for {{.Name}}"
                onclick="event.stopPropagation(); document.getElementById('folder-edit-{{.NodeID}}').showModal()"
                style="margin-left:auto;flex:none;border:none;background:none;padding:2px 6px;cursor:pointer;color:#9CA3AF;font-size:14px;line-height:1">&middot;&middot;&middot;</button>
        {{end}}
      </div>
      <div data-sort="{{if .ScoreValue}}{{.ScoreValue}}{{end}}" style="display:flex;align-items:center;gap:7px;min-width:0">
        <span class="lotus-text-{{.BarTone}}" style="font:600 11px 'IBM Plex Mono',monospace;white-space:nowrap">{{.Score}}</span>
        <span style="flex:1;height:4px;border-radius:2px;background:#F3F4F6;overflow:hidden;display:block">
          <span class="lotus-bar-{{.BarTone}}" style="display:block;height:100%;width:{{.ScorePct}}"></span>
        </span>
      </div>
      {{range .Cells}}<div data-sort="{{if .Sort}}{{.Sort}}{{end}}" style="display:flex;justify-content:center;min-width:0">{{template "status_badge" .}}</div>{{end}}
    </div>
    {{else}}
    <a href="{{.Href}}" class="lotus-repo-row" style="text-decoration:none;color:inherit"
       {{if $.Tree}}data-node="{{.NodeID}}" data-parent="{{.ParentID}}" data-ancestors="{{.Ancestors}}"
       x-show="(filter==='all' || filter==='{{.Tone}}') && !hiddenBy($el.dataset.ancestors)"{{else}}x-show="filter==='all' || filter==='{{.Tone}}'"{{end}}>
      <div style="font:400 10.5px 'IBM Plex Mono',monospace;color:#9CA3AF">{{.N}}</div>
      <div data-sort="{{.Name}}" style="display:flex;align-items:center;gap:8px;min-width:0;padding-left:{{.IndentPx}}px">
        {{template "status_dot" .Tone}}
        <span style="font-size:12.5px;font-weight:600;color:#111827;overflow:hidden;text-overflow:ellipsis;white-space:nowrap">{{.Name}}</span>
        {{if .Subtext}}<span style="font:400 9.5px 'IBM Plex Mono',monospace;color:#9CA3AF;flex:none;overflow:hidden;text-overflow:ellipsis">{{.Subtext}}</span>{{end}}
      </div>
      <div data-sort="{{if .ScoreValue}}{{.ScoreValue}}{{end}}" style="display:flex;align-items:center;gap:7px">
        <span class="lotus-text-{{.Tone}}" style="font:600 12px 'IBM Plex Mono',monospace;width:20px">{{.Score}}</span>
        <span style="flex:1;height:4px;border-radius:2px;background:#F3F4F6;overflow:hidden;display:block">
          <span class="lotus-bar-{{.Tone}}" style="display:block;height:100%;width:{{.ScorePct}}"></span>
        </span>
      </div>
      {{if .TrendURL}}
      <div style="min-width:0;padding:0 6px" hx-get="{{.TrendURL}}" hx-trigger="revealed once" hx-swap="innerHTML">
        <span style="display:block;height:26px"></span>
      </div>
      {{end}}
      {{range .Cells}}<div data-sort="{{if .Sort}}{{.Sort}}{{end}}" style="display:flex;justify-content:center;min-width:0">{{template "status_badge" .}}</div>{{end}}
    </a>
    {{end}}
    {{end}}
```

Folder rows never render a trend cell: the tree only exists on the organization dashboard, where `showTrendColumn` is false.

- [ ] **Step 5: Create the folder dialogs**

Create `web/templates/components/folder_dialogs.html`:

```html
{{/* folder_create_form is the New folder dialog's body. Takes "." = the
     organization dashboard's view model (for FolderSelect). */}}
{{define "folder_create_form"}}
<div class="lotus-modal-form">
  <form method="post" action="/folders" hx-post="/folders" hx-target="#new-folder-error" hx-swap="innerHTML">
    <div class="lotus-modal-body" style="display:flex;flex-direction:column;gap:14px">
      <div id="new-folder-error"></div>
      <label style="display:flex;flex-direction:column;gap:6px">
        <span style="font:600 10.5px 'IBM Plex Mono',monospace;letter-spacing:.08em;color:var(--text-label)">NAME</span>
        <input type="text" name="name" required maxlength="80" placeholder="IT Services" class="lotus-input">
      </label>
      <label style="display:flex;flex-direction:column;gap:6px">
        <span style="font:600 10.5px 'IBM Plex Mono',monospace;letter-spacing:.08em;color:var(--text-label)">INSIDE</span>
        {{template "lotus_select" (dict "Name" "parent_id" "ID" "new-folder-parent" "Value" "" "Label" "Inside" "Options" (selectOptions .FolderSelect))}}
      </label>
    </div>
    <div class="lotus-modal-foot">
      <button type="button" class="btn-lotus btn-lotus-secondary" onclick="this.closest('dialog').close()">Cancel</button>
      <button type="submit" class="btn-lotus btn-lotus-primary">Create folder</button>
    </div>
  </form>
</div>
{{end}}

{{/* folder_edit_dialog is one folder's actions: rename, move, delete.
     One dialog per folder, rendered server-side, so each carries its own
     move targets (never itself or its subtree, never past the depth cap)
     and a fixed form action. Opened by the row's actions button.
     Takes (dict "Row" <folder dashboardRow> "CanDelete" bool). */}}
{{define "folder_edit_dialog"}}
{{$r := .Row}}
<div x-data="lotusModal('folder-{{$r.NodeID}}')" style="display:contents">
  <dialog class="lotus-modal lotus-shape" x-ref="dialog" id="folder-edit-{{$r.NodeID}}" aria-labelledby="folder-edit-{{$r.NodeID}}-title">
    <div class="lotus-modal-head">
      <div class="lotus-modal-title" id="folder-edit-{{$r.NodeID}}-title">{{$r.Name}}</div>
      <button type="button" class="lotus-modal-close" @click="close()" aria-label="Close">
        <svg width="14" height="14" viewBox="0 0 24 24" fill="none" aria-hidden="true"><path fill="none" stroke="currentColor" stroke-linecap="round" stroke-linejoin="round" stroke-width="1.5" d="M12 4v16m8-8H4"></path></svg>
      </button>
    </div>
    <div class="lotus-modal-body" style="display:flex;flex-direction:column;gap:18px">
      <div id="folder-error-{{$r.NodeID}}"></div>

      <form method="post" action="/folders/{{$r.NodeID}}/rename" hx-post="/folders/{{$r.NodeID}}/rename"
            hx-target="#folder-error-{{$r.NodeID}}" hx-swap="innerHTML" style="display:flex;flex-direction:column;gap:6px">
        <span style="font:600 10.5px 'IBM Plex Mono',monospace;letter-spacing:.08em;color:var(--text-label)">NAME</span>
        <div style="display:flex;gap:8px">
          <input type="text" name="name" required maxlength="80" value="{{$r.Name}}" class="lotus-input" style="flex:1" aria-label="Folder name">
          <button type="submit" class="btn-lotus btn-lotus-secondary">Rename</button>
        </div>
      </form>

      <form method="post" action="/folders/{{$r.NodeID}}/move" hx-post="/folders/{{$r.NodeID}}/move"
            hx-target="#folder-error-{{$r.NodeID}}" hx-swap="innerHTML" style="display:flex;flex-direction:column;gap:6px">
        <span style="font:600 10.5px 'IBM Plex Mono',monospace;letter-spacing:.08em;color:var(--text-label)">MOVE INTO</span>
        <div style="display:flex;gap:8px;align-items:center">
          <div style="flex:1;min-width:0">
            {{template "lotus_select" (dict "Name" "parent_id" "ID" (printf "move-%s" $r.NodeID) "Value" $r.MoveValue "Label" "Move into" "Options" (selectOptions $r.MoveOptions))}}
          </div>
          <button type="submit" class="btn-lotus btn-lotus-secondary">Move</button>
        </div>
      </form>

      {{if .CanDelete}}
      <form method="post" action="/folders/{{$r.NodeID}}/delete" hx-post="/folders/{{$r.NodeID}}/delete"
            hx-target="#folder-error-{{$r.NodeID}}" hx-swap="innerHTML" hx-confirm="Delete the folder {{$r.Name}}?"
            style="display:flex;align-items:center;gap:10px;padding-top:4px;border-top:1px solid #F3F4F6">
        <button type="submit" class="btn-lotus btn-lotus-secondary" {{if $r.DeleteBlocked}}disabled{{end}}>Delete folder</button>
        {{if $r.DeleteBlocked}}<span style="font-size:11.5px;color:#6B7280">{{$r.DeleteBlocked}}</span>{{end}}
      </form>
      {{end}}
    </div>
  </dialog>
</div>
{{end}}
```

- [ ] **Step 6: Wire the page**

In `web/templates/pages/projects.html`, inside the `{{else}}` branch (the non-empty dashboard):

1. Replace `<div x-data="{filter:'all'}">` with `<div x-data="lotusTree()">`.

2. After the `<a href="/projects/new" …>New project</a>` element and before the closing `</div>` of the button group, add:

```html
      {{if .Data.CanCreateFolder}}
      {{template "lotus_modal_open" (dict "Name" "new-folder" "Title" "New folder" "Trigger" "New folder")}}
        {{template "folder_create_form" .Data}}
      {{template "lotus_modal_close" .}}
      {{end}}
```

3. After `{{template "dashboard_block" .Data}}` and before the closing `</div>` of the `lotusTree()` element, add:

```html
  {{if .Data.CanEditFolder}}
  {{range .Data.Rows}}{{if .IsFolder}}
  {{template "folder_edit_dialog" (dict "Row" . "CanDelete" $.Data.CanDeleteFolder)}}
  {{end}}{{end}}
  {{end}}
```

- [ ] **Step 7: Add `lotusTree` and make sorting tree-aware**

In `web/static/js/lotus.js`:

1. Replace the body of `apply()` in `lotusSort` with:

```js
    apply() {
      const box = this.$refs.rows;
      if (!box) return;
      const rows = Array.from(box.children);
      const keyOf = (row) => {
        const cell = row.children[this.col];
        return cell ? (cell.dataset.sort || '') : '';
      };
      const compare = (a, b) => {
        const av = keyOf(a), bv = keyOf(b);
        // A cell with no value ("no data", or a status-only kind) sorts
        // last in BOTH directions. Treating it as zero would rank
        // unmeasured repositories as the worst ones, which is a different
        // claim entirely.
        const aEmpty = av === '', bEmpty = bv === '';
        if (aEmpty !== bEmpty) return aEmpty ? 1 : -1;
        if (aEmpty) return 0;
        if (this.kind === 'num') return (parseFloat(av) - parseFloat(bv)) * this.dir;
        return av.localeCompare(bv, undefined, { sensitivity: 'base' }) * this.dir;
      };

      // On the folder tree a row sorts among its siblings only, and each
      // folder's rows stay directly beneath it — a flat sort by score
      // would scatter projects away from the folders that hold them.
      // Folders stay ahead of projects at every level.
      let ordered;
      if (rows.some((r) => r.dataset.node)) {
        const byParent = new Map();
        rows.forEach((r) => {
          const p = r.dataset.parent || '';
          if (!byParent.has(p)) byParent.set(p, []);
          byParent.get(p).push(r);
        });
        ordered = [];
        const seen = new Set();
        const walk = (parent) => {
          const kids = (byParent.get(parent) || []).slice().sort((a, b) => {
            const af = a.dataset.folder === '1', bf = b.dataset.folder === '1';
            if (af !== bf) return af ? -1 : 1;
            return compare(a, b);
          });
          kids.forEach((k) => {
            if (seen.has(k)) return;
            seen.add(k);
            ordered.push(k);
            if (k.dataset.folder === '1') walk(k.dataset.node);
          });
        };
        walk('');
      } else {
        ordered = rows.sort(compare);
      }

      ordered.forEach((r) => box.appendChild(r));
      this.retitle();
      this.renumber();
    },
```

2. After the closing `}` of `function lotusSort`, add:

```js
// lotusTree is the organization dashboard's page state: the Failing filter
// (read by dashboard_block's rows) and which folders are collapsed.
//
// Collapsed folders are remembered per browser. Storage can be missing or
// throw — a private window, blocked site data — so every access is guarded:
// the tree then starts expanded and collapsing still works, it just is not
// remembered.
const LOTUS_TREE_KEY = 'lotus.tree.collapsed';

function lotusTree() {
  return {
    filter: 'all',
    collapsed: {},

    init() {
      try {
        const saved = JSON.parse(window.localStorage.getItem(LOTUS_TREE_KEY) || '{}');
        if (saved && typeof saved === 'object') this.collapsed = saved;
      } catch (e) {
        this.collapsed = {};
      }
    },

    isCollapsed(id) {
      return !!this.collapsed[id];
    },

    toggle(id) {
      const next = { ...this.collapsed };
      if (next[id]) delete next[id];
      else next[id] = true;
      this.collapsed = next;
      try {
        window.localStorage.setItem(LOTUS_TREE_KEY, JSON.stringify(next));
      } catch (e) {
        // Not remembered; the collapse itself still happened.
      }
    },

    // hiddenBy: is any of these space-separated folder ids collapsed?
    hiddenBy(ancestors) {
      return (ancestors || '').split(' ').some((id) => id && this.collapsed[id]);
    },
  };
}
```

- [ ] **Step 8: Run the page tests and the web suite**

Run: `go test ./internal/web -v -run 'TestProjectsPage_|TestDashboard_|TestKindOrder|TestRouteCoverage'`
Expected: PASS.

Then: `go test ./internal/web`
Expected: PASS.

- [ ] **Step 9: Look at the page in a browser**

Run `make css`. Restart the dev server if it isn't running `make dev`. Then log in at http://localhost:8008/login as `owner@lotus.test`. Create "Product", move AlurKerja into it from its project page (Task 8 adds that button; until then, use `psql` to set `folder_id`), and check:
- the folder row shows "N / M passing" with a bar, and the chevron rotates when you click the row;
- clicking a folder row hides its contents, and they stay hidden after a reload;
- the ··· button opens the dialog without collapsing the row.

- [ ] **Step 10: Commit**

```bash
git add web/templates/components/dashboard_block.html web/templates/components/select.html web/templates/components/folder_dialogs.html web/templates/pages/projects.html web/static/js/lotus.js internal/web/folders_page_test.go
git commit -m "feat(web): render the folder tree with collapse, sorting and folder dialogs"
```

---

### Task 8: Web — move a project, and file a new one

**Files:**
- Modify: `internal/web/project_page.go:20-72` (`projectPage`)
- Modify: `web/templates/pages/project.html:4-12` (the header)
- Modify: `internal/web/project_new.go` (the form data, `newProjectPage`, `submitNewProject` and the render helpers)
- Modify: `web/templates/pages/project_new.html` (add a folder picker)
- Test: `internal/web/project_folder_test.go`

**Interfaces:**
- Consumes: `repo.ListFolders`, `folderOptionsFor`, `folderSelect`, `NewProject.FolderID`, and the `POST /projects/:id/move` route (Task 6).
- Produces: a "Move to folder" dialog on the project page (form field `folder_id`); a `folder_id` field on `POST /projects/new`.

- [ ] **Step 1: Write the failing tests**

Create `internal/web/project_folder_test.go`:

```go
package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

func TestProjectPage_MoveToFolderIsOfferedToMembersOnly(t *testing.T) {
	for _, tc := range []struct {
		role string
		want bool
	}{{"member", true}, {"viewer", false}} {
		t.Run(tc.role, func(t *testing.T) {
			app, db := newDashboardTestApp(t)
			sub := "acme-pf-" + tc.role
			orgID, userID := seedMember(t, db, sub, tc.role)
			raw := seedSession(t, db, orgID, userID)
			var projectID uuid.UUID
			withOrgTx(t, db, orgID, func(tx *sqlx.Tx) {
				if _, err := tx.Exec(`INSERT INTO folders (org_id, name) VALUES ($1, 'Product')`, orgID); err != nil {
					t.Fatalf("insert folder: %v", err)
				}
				if err := tx.Get(&projectID, `INSERT INTO projects (org_id, slug, name) VALUES ($1,'aos','AOS Online') RETURNING id`, orgID); err != nil {
					t.Fatalf("insert project: %v", err)
				}
			})
			resp, err := app.Test(reqWithSession(http.MethodGet, sub+"."+dashboardTestDomain, "/projects/"+projectID.String(), raw))
			if err != nil || resp.StatusCode != http.StatusOK {
				t.Fatalf("GET project = %v, %v", resp, err)
			}
			body, _ := io.ReadAll(resp.Body)
			if got := strings.Contains(string(body), "Move to folder"); got != tc.want {
				t.Fatalf("Move to folder shown = %v, want %v", got, tc.want)
			}
			if tc.want && !strings.Contains(string(body), "Product") {
				t.Fatal("folder picker does not list Product")
			}
		})
	}
}

func TestNewProject_CanBeFiledInAFolder(t *testing.T) {
	app, db := newDashboardTestApp(t)
	orgID, userID := seedMember(t, db, "acme-np", "member")
	raw := seedSession(t, db, orgID, userID)
	host := "acme-np." + dashboardTestDomain
	var folderID uuid.UUID
	withOrgTx(t, db, orgID, func(tx *sqlx.Tx) {
		if err := tx.Get(&folderID, `INSERT INTO folders (org_id, name) VALUES ($1, 'Product') RETURNING id`, orgID); err != nil {
			t.Fatalf("insert folder: %v", err)
		}
	})

	getResp, _ := app.Test(reqWithSession(http.MethodGet, host, "/projects/new", raw))
	page, _ := io.ReadAll(getResp.Body)
	if !strings.Contains(string(page), `name="folder_id"`) {
		t.Fatal("new project form has no folder picker")
	}

	form := url.Values{"name": {"SyarQ"}, "slug": {"syarq"}, "folder_id": {folderID.String()}}
	req := httptest.NewRequest(http.MethodPost, "http://"+host+"/projects/new", strings.NewReader(form.Encode()))
	req.Host = host
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "lotus_session", Value: raw})
	resp, err := app.Test(req)
	if err != nil || resp.StatusCode != http.StatusFound {
		t.Fatalf("create = %v, %v; want 302", resp, err)
	}
	var got *uuid.UUID
	withOrgTx(t, db, orgID, func(tx *sqlx.Tx) {
		if err := tx.Get(&got, `SELECT folder_id FROM projects WHERE slug = 'syarq'`); err != nil {
			t.Fatalf("read project: %v", err)
		}
	})
	if got == nil || *got != folderID {
		t.Fatalf("folder_id = %v, want %v", got, folderID)
	}
}
```

- [ ] **Step 2: Run the tests and confirm they fail**

Run: `go test ./internal/web -run 'TestProjectPage_MoveToFolder|TestNewProject_CanBeFiled' -v`
Expected: FAIL: `Move to folder shown = false, want true` and `new project form has no folder picker`.

- [ ] **Step 3: Load folders on the project page**

In `internal/web/project_page.go`, inside `projectPage`, before `c.Type("html")`, add:

```go
		folders, err := repo.ListFolders(c.Context(), s)
		if err != nil {
			return writeRepoErr(c, err)
		}
		folderValue := ""
		if project.FolderID != nil {
			folderValue = project.FolderID.String()
		}
```

Replace the anonymous `Data` struct with:

```go
			Data: struct {
				Project      repo.Project
				Dashboard    dashboardViewModel
				CanMove      bool
				FolderSelect []httpx.SelectOption
				FolderValue  string
			}{
				Project: *project, Dashboard: vm,
				CanMove:      mustPrincipal(c).Can("project.update"),
				FolderSelect: folderSelect(folderOptionsFor(folders), nil),
				FolderValue:  folderValue,
			},
```

- [ ] **Step 4: Add the dialog to the project page header**

In `web/templates/pages/project.html`, inside `<header …>`, directly before the `<a href="/projects/{{.Data.Project.ID}}/components/new" …>Add component</a>` element, add:

```html
  {{if .Data.CanMove}}
  {{template "lotus_modal_open" (dict "Name" "move-project" "Title" "Move to folder" "Trigger" "Move to folder")}}
    <div class="lotus-modal-form">
      <form method="post" action="/projects/{{.Data.Project.ID}}/move" hx-post="/projects/{{.Data.Project.ID}}/move"
            hx-target="#move-project-error" hx-swap="innerHTML">
        <div class="lotus-modal-body" style="display:flex;flex-direction:column;gap:14px">
          <div id="move-project-error"></div>
          <label style="display:flex;flex-direction:column;gap:6px">
            <span style="font:600 10.5px 'IBM Plex Mono',monospace;letter-spacing:.08em;color:var(--text-label)">FOLDER</span>
            {{template "lotus_select" (dict "Name" "folder_id" "Value" .Data.FolderValue "Label" "Folder" "Options" (selectOptions .Data.FolderSelect))}}
          </label>
        </div>
        <div class="lotus-modal-foot">
          <button type="button" class="btn-lotus btn-lotus-secondary" onclick="this.closest('dialog').close()">Cancel</button>
          <button type="submit" class="btn-lotus btn-lotus-primary">Move</button>
        </div>
      </form>
    </div>
  {{template "lotus_modal_close" .}}
  {{end}}
```

- [ ] **Step 5: Add a folder picker to New project**

In `internal/web/project_new.go`:

1. Extend the form data:

```go
type newProjectFormData struct {
	Slug, Name, Description, Error string
	FolderID                       string
	FolderSelect                   []httpx.SelectOption
}
```

2. Make `newProjectPage` render through the helper that loads data. Replace its body after `s, err := mustScope(c)` … with:

```go
		return renderNewProjectFormWithCount(c, d, s, org, user, newProjectFormData{}, fiber.StatusOK)
```

(Delete its now-unused `projects` lookup.)

3. In `submitNewProject`, read the folder and pass it through:

```go
		form := newProjectFormData{
			Name:        strings.TrimSpace(c.FormValue("name")),
			Slug:        strings.TrimSpace(c.FormValue("slug")),
			Description: strings.TrimSpace(c.FormValue("description")),
			FolderID:    strings.TrimSpace(c.FormValue("folder_id")),
		}
		folderID, err := optionalUUID(form.FolderID)
		if err != nil {
			form.Error = "that folder no longer exists"
			return renderNewProjectFormWithCount(c, d, s, org, user, form, fiber.StatusUnprocessableEntity)
		}
```

Change the create call to pass `FolderID: folderID`. In its error switch, add a case before `default`:

```go
			case errors.Is(err, repo.ErrNotFound):
				form.Error = "that folder no longer exists"
```

4. In `renderNewProjectFormWithCount`, load the folders before rendering:

```go
	folders, err := repo.ListFolders(c.Context(), s)
	if err != nil {
		return writeRepoErr(c, err)
	}
	form.FolderSelect = folderSelect(folderOptionsFor(folders), nil)
```

In `web/templates/pages/project_new.html`, after the DESCRIPTION `<label>` block, add:

```html
    <label style="display:flex;flex-direction:column;gap:6px">
      <span style="font:600 10.5px 'IBM Plex Mono',monospace;letter-spacing:.08em;color:#5B4A63">FOLDER</span>
      {{template "lotus_select" (dict "Name" "folder_id" "Value" .Data.FolderID "Label" "Folder" "Options" (selectOptions .Data.FolderSelect))}}
    </label>
```

- [ ] **Step 6: Run the tests and the web suite**

Run: `go test ./internal/web -run 'TestProjectPage_MoveToFolder|TestNewProject' -v && go test ./internal/web`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/web/project_page.go internal/web/project_new.go internal/web/project_folder_test.go web/templates/pages/project.html web/templates/pages/project_new.html
git commit -m "feat(web): move a project to a folder, and file new projects on creation"
```

---

### Task 9: End-to-end — the browser golden path

**Files:**
- Create: `e2e/tests/folders.spec.ts`

**Interfaces:**
- Consumes: the DOM contract from Task 7 (`data-folder="1"`, `a.lotus-repo-row`, `folder-edit-<id>`), the "New folder", "Create folder", "Move to folder" and "Move" controls, `seedOwnerSession`, `withDB`, `retireOrg`.

- [ ] **Step 1: Write the spec**

Create `e2e/tests/folders.spec.ts`:

```ts
import { expect, test, type Page } from '@playwright/test';
import { retireOrg, seedOwnerSession, withDB } from './helpers/db';

const PORT = process.env.LOTUS_E2E_PORT ?? '8095';
const BASE_DOMAIN = process.env.LOTUS_BASE_DOMAIN ?? 'lotus.localhost';
const tenantOrigin = (s: string) => `http://${s}.${BASE_DOMAIN}:${PORT}`;

// seedProject inserts a project with one component whose SonarQube
// headline — and so the project's Lotus status — is `status` at `score`.
async function seedProject(orgId: string, name: string, status: 'pass' | 'fail', score: number): Promise<string> {
  return withDB(async (c) => {
    const p = await c.query(`INSERT INTO projects (org_id, slug, name) VALUES ($1, $2, $3) RETURNING id`,
      [orgId, name.toLowerCase(), name]);
    const comp = await c.query(`INSERT INTO components (org_id, project_id, slug, name, kind) VALUES ($1, $2, 'api', $3, 'backend') RETURNING id`,
      [orgId, p.rows[0].id, `${name}-api`]);
    await c.query(
      `INSERT INTO component_metrics_current (org_id, project_id, component_id, metrics, status, score, updated_at)
       VALUES ($1, $2, $3, $4::jsonb, $5, $6, now())`,
      [orgId, p.rows[0].id, comp.rows[0].id, JSON.stringify({ sonarqube: { status, score } }), status, score]);
    return p.rows[0].id as string;
  });
}

// Pick an option in the dropdown inside the dialog that is currently open.
// Scoped to the open dialog because every folder dialog on the page carries
// its own parent_id picker.
async function chooseInOpenDialog(page: Page, label: string) {
  const dialog = page.locator('dialog[open]');
  await dialog.locator('.lotus-combobox').getByRole('combobox').click();
  await dialog.getByRole('option', { name: label, exact: true }).click();
}

const folderRow = (page: Page, name: string) =>
  page.locator('[data-folder="1"]').filter({ has: page.getByText(name, { exact: true }) });
const projectRow = (page: Page, name: string) =>
  page.locator('a.lotus-repo-row').filter({ has: page.getByText(name, { exact: true }) });

test('folders roll up passing projects and remember being collapsed', async ({ page, context }) => {
  const { sessionToken, subdomain, orgId } = await seedOwnerSession('folders-e2e', 'placeholder');
  const origin = tenantOrigin(subdomain);
  await context.addCookies([{ name: 'lotus_session', value: sessionToken, url: origin }]);
  const linknet = await seedProject(orgId, 'Linknet', 'fail', 42);
  const dignas = await seedProject(orgId, 'Dignas', 'pass', 95);

  await page.goto(`${origin}/projects`);

  await page.getByRole('button', { name: 'New folder' }).click();
  await page.locator('dialog[open] input[name="name"]').fill('IT Services');
  await page.getByRole('button', { name: 'Create folder' }).click();
  await expect(folderRow(page, 'IT Services')).toBeVisible();

  await page.getByRole('button', { name: 'New folder' }).click();
  await page.locator('dialog[open] input[name="name"]').fill('Tribe lead A');
  await chooseInOpenDialog(page, 'IT Services');
  await page.getByRole('button', { name: 'Create folder' }).click();
  await expect(folderRow(page, 'Tribe lead A')).toBeVisible();

  for (const id of [linknet, dignas]) {
    await page.goto(`${origin}/projects/${id}`);
    await page.getByRole('button', { name: 'Move to folder' }).click();
    await chooseInOpenDialog(page, 'IT Services / Tribe lead A');
    await page.locator('dialog[open]').getByRole('button', { name: 'Move', exact: true }).click();
    await expect(page.getByText('Project moved')).toBeVisible();
  }

  await page.goto(`${origin}/projects`);
  await expect(folderRow(page, 'Tribe lead A')).toContainText('1 / 2 passing');
  await expect(folderRow(page, 'Tribe lead A')).toContainText('failing: Linknet 42');
  await expect(folderRow(page, 'IT Services')).toContainText('1 / 2 passing');

  // Collapse Tribe lead A: its projects hide, and stay hidden after a reload.
  await folderRow(page, 'Tribe lead A').getByText('Tribe lead A', { exact: true }).click();
  await expect(projectRow(page, 'Linknet')).toBeHidden();
  await page.reload();
  await expect(projectRow(page, 'Linknet')).toBeHidden();
  await expect(folderRow(page, 'Tribe lead A')).toBeVisible();

  // Expanding again brings them back.
  await folderRow(page, 'Tribe lead A').getByText('Tribe lead A', { exact: true }).click();
  await expect(projectRow(page, 'Linknet')).toBeVisible();

  await retireOrg(orgId);
});

// Review focus: with storage blocked the tree still renders and collapses;
// it only forgets on reload.
test('the tree still collapses when localStorage is unavailable', async ({ page, context }) => {
  const { sessionToken, subdomain, orgId } = await seedOwnerSession('folders-nostore-e2e', 'placeholder');
  const origin = tenantOrigin(subdomain);
  await context.addCookies([{ name: 'lotus_session', value: sessionToken, url: origin }]);
  const pid = await seedProject(orgId, 'ANRI', 'pass', 90);
  await withDB(async (c) => {
    const f = await c.query(`INSERT INTO folders (org_id, name) VALUES ($1, 'Tribe lead B') RETURNING id`, [orgId]);
    await c.query(`UPDATE projects SET folder_id = $1 WHERE id = $2`, [f.rows[0].id, pid]);
  });

  await page.addInitScript(() => {
    Object.defineProperty(window, 'localStorage', {
      configurable: true,
      get() { throw new Error('storage blocked'); },
    });
  });
  const errors: string[] = [];
  page.on('pageerror', (e) => errors.push(e.message));

  await page.goto(`${origin}/projects`);
  await expect(projectRow(page, 'ANRI')).toBeVisible();
  await folderRow(page, 'Tribe lead B').getByText('Tribe lead B', { exact: true }).click();
  await expect(projectRow(page, 'ANRI')).toBeHidden();
  expect(errors).toEqual([]);

  await retireOrg(orgId);
});
```

- [ ] **Step 2: Run the spec**

Run: `make e2e-db && cd e2e && DATABASE_URL='postgres://postgres@localhost:5418/lotus_e2e?sslmode=disable' LOTUS_MODE=saas npx playwright test tests/folders.spec.ts`
Expected: 2 passed. If a step fails, open the trace (`npx playwright show-trace test-results/…/trace.zip`) before changing anything. Selector drift in the dialog markup is the most likely cause.

- [ ] **Step 3: Run the whole e2e suite**

Run: `make e2e`
Expected: every spec passes. In particular `dashboard.spec.ts`, whose "No repositories yet" empty state must be unchanged for an organization with no folders.

- [ ] **Step 4: Run the full Go suite**

Run: `make test`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add e2e/tests/folders.spec.ts
git commit -m "test(e2e): folders roll up and collapse, with and without storage"
```
