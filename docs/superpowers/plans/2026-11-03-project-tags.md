# Project Tags Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace folders in the UI with a flat project list grouped by tags, with an All / tag filter on `/dashboard` and `/projects`.

**Architecture:** Two new tenant tables (`tags`, `project_tags`) under the same forced RLS as every tenant table, one new permission `project.tag` (owner, admin), and a migration that turns folder `ITSD` into tag `PROJECT` and folder `PRODUCT` into tag `PRODUCT`. The folder tables stay; every UI path that reads or writes them is removed. One web helper parses `?tag=` and builds the chips; both list pages narrow their project set through it.

**Tech Stack:** Go 1.x, Fiber, sqlx, goose migrations, html/template, Alpine.js, htmx, Tailwind v4, Playwright.

**Spec:** `docs/superpowers/specs/2026-11-03-tags-and-status-page-design.md` §1–§5, §7. (§6, the status page, is the second plan: `2026-11-03-public-status-page.md`.)

## Global Constraints

- Kind order is fixed: SonarQube, Trivy, Sentry, GitLab, from `internal/web/bindview.go → bindableKinds`.
- Alpine `:style` uses the object form, never a string.
- DB schema and data of `folders` and `projects.folder_id` stay untouched; only the UI stops using them.
- Tags at launch: exactly `PROJECT` and `PRODUCT`. Names are shown as written; unique per organization ignoring case.
- Tag editing: `owner` and `admin` only (`project.tag`).
- Filter semantics: a project matches when it carries **any** chosen tag. **All** is the default and clears the selection. Selection in the URL as repeated `tag` parameters: `?tag=PROJECT&tag=PRODUCT`.
- Production runs Postgres 16.
- Every commit: atomic, Conventional Commits (`type(scope): summary`, body says why), passes `make lint` and `make test`; ends with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`. Commit only when the user has asked for commits in this session.
- E2E for every user-visible behavior (`make e2e`).

## Review Focus

1. `?tag=` naming a tag that does not exist (old bookmark, typo, tag removed since): the page must not 500 or show an empty fleet with no way out. Unknown names are dropped; if none survive, the page behaves as All. Test in Task 3.
2. Tag names differing only in case or surrounding spaces (`project`, ` PROJECT `): adding must reuse the existing `PROJECT`, not create a second tag. Test in Task 2.
3. Removing the last project from a tag: the chip must disappear from the filter rather than linger as a chip that always filters to nothing. Test in Task 2.
4. Old folder URLs (`/projects?folder=<id>`, bookmarked before the change): must still render the flat list, not 404. Test in Task 4.
5. A member (not owner/admin) posting to the tag routes directly: must be 403 even though the UI hides the controls. Test in Task 5.

---

### Task 1: Tag tables, permission and the folder→tag migration

**Files:**
- Create: `internal/db/migrations/00039_project_tags.sql`
- Modify: `internal/authz/principal.go` (add `"project.tag"` to `owner` and `admin` sets)
- Test: `internal/db/project_tags_test.go`

**Interfaces:**
- Produces: tables `tags (id uuid PK, org_id, name text, created_at, UNIQUE (id, org_id))` with unique index `tags_org_name` on `(org_id, lower(name))`; `project_tags (org_id, project_id, tag_id, PRIMARY KEY (project_id, tag_id))` with composite FKs `(project_id, org_id) → projects(id, org_id) ON DELETE CASCADE` and `(tag_id, org_id) → tags(id, org_id) ON DELETE CASCADE`. Permission key `project.tag`.

- [ ] **Step 1: Write the failing test** `TestProjectTags_FoldersBecomeTags` following `internal/db/one_binding_per_kind_test.go`: `db.MigrateDownTo(dsn, 38)`, seed one org with folders `ITSD` (2 projects), `PRODUCT` (1 project), `Other` (1 project) and one project with no folder, then `db.Migrate(dsn)`. Assert:
  - tags for the org are exactly `PROJECT`, `PRODUCT` (no tag for `Other`);
  - both ITSD projects carry `PROJECT`, the PRODUCT project carries `PRODUCT`, the other two carry none;
  - a soft-deleted folder named `ITSD` (`deleted_at` set) contributes no tag;
  - `folders` row count and every `projects.folder_id` are unchanged.
  Also `TestProjectTags_ForceRLS` asserting `relforcerowsecurity` is true for both tables (pattern: `TestQASchema_EveryTableForcesRLS`), and `TestProjectTags_TagNameUniqueIgnoringCase` (inserting `project` after `PROJECT` in the same org fails with 23505; the same name in another org succeeds).

- [ ] **Step 2: Run it to make sure it fails**
  Run: `go test ./internal/db -run 'TestProjectTags' -v`
  Expected: FAIL (`relation "tags" does not exist`).

- [ ] **Step 3: Write the migration.** Pattern: `00026_folders.sql` (RLS `DO $$` block, `GRANT … TO lotus_app`, `role_permissions` seed with matching Down). Name check: `btrim(name) <> '' AND char_length(name) <= 40`. Data step, after the tables:
  ```sql
  INSERT INTO tags (org_id, name)
  SELECT DISTINCT f.org_id, m.tag FROM folders f
  JOIN (VALUES ('ITSD', 'PROJECT'), ('PRODUCT', 'PRODUCT')) AS m (folder, tag) ON lower(f.name) = lower(m.folder)
  WHERE f.deleted_at IS NULL AND f.parent_id IS NULL;
  INSERT INTO project_tags (org_id, project_id, tag_id)
  SELECT p.org_id, p.id, t.id FROM projects p
  JOIN folders f ON f.id = p.folder_id AND f.deleted_at IS NULL AND f.parent_id IS NULL
  JOIN (VALUES ('ITSD', 'PROJECT'), ('PRODUCT', 'PRODUCT')) AS m (folder, tag) ON lower(f.name) = lower(m.folder)
  JOIN tags t ON t.org_id = p.org_id AND lower(t.name) = lower(m.tag)
  WHERE p.deleted_at IS NULL;
  ```
  Down drops both tables and deletes the `project.tag` permission rows.

- [ ] **Step 4: Add `"project.tag"` to the owner and admin sets in `rolePermissions`.**

- [ ] **Step 5: Run the tests**
  Run: `go test ./internal/db ./internal/authz -v`
  Expected: PASS, including the existing `TestRolePermissionsSeedMatchesTheGoMatrix` and `TestMigrateUpIsIdempotent`.

- [ ] **Step 6: Commit** `feat(projects): add project tags and turn the two folders into tags`

### Task 2: Tag repository

**Files:**
- Create: `internal/repo/tags.go`
- Test: `internal/repo/tags_test.go`

**Interfaces:**
- Consumes: Task 1 tables.
- Produces:
  - `type Tag struct { ID uuid.UUID \`db:"id"\`; Name string \`db:"name"\` }`
  - `var ErrInvalidTag = errors.New("tag name must be 1–40 characters")`
  - `func ListTags(ctx context.Context, s tenant.Scope) ([]Tag, error)` — tags carried by at least one live (not deleted) project, ordered by `lower(name)`.
  - `func ProjectTagsByProject(ctx context.Context, s tenant.Scope) (map[uuid.UUID][]Tag, error)` — every live project's tags, each slice ordered by `lower(name)`.
  - `func AddProjectTag(ctx context.Context, s tenant.Scope, projectID uuid.UUID, name string) (Tag, error)` — trims; rejects empty or >40 runes with `ErrInvalidTag`; reuses an existing tag matched case-insensitively (keeping its stored spelling), else creates it; adding a tag the project already has is a no-op returning it.
  - `func RemoveProjectTag(ctx context.Context, s tenant.Scope, projectID, tagID uuid.UUID) error` — removes the link; deletes the tag in the same transaction when no project carries it any more (design call for spec §5.2: orphans are cleaned up).
  - `func ProjectIDsWithAnyTag(ctx context.Context, s tenant.Scope, tagIDs []uuid.UUID) ([]uuid.UUID, error)` — live projects carrying any of tagIDs.

- [ ] **Step 1: Write the failing tests** (pattern: existing `internal/repo/*_test.go` with `dbtest`):
  - `TestAddProjectTag_ReusesTheTagIgnoringCaseAndSpaces`: add `PROJECT` to p1, then ` project ` to p2 → same `ID`, `Name == "PROJECT"`, `ListTags` has one entry.
  - `TestAddProjectTag_RejectsEmptyAndTooLong`: `"  "` and 41×`"x"` → `ErrInvalidTag`.
  - `TestRemoveProjectTag_DropsTheTagWithItsLastProject`: tag on p1 and p2; remove from p1 → still listed; remove from p2 → `ListTags` empty.
  - `TestProjectIDsWithAnyTag_IsAnyOf`: p1 `PROJECT`, p2 `PRODUCT`, p3 none → `{PROJECT, PRODUCT}` returns p1, p2; `{PROJECT}` returns p1; a soft-deleted project carrying `PROJECT` is never returned.
  - `TestTags_AreTenantScoped`: a tag in org B is invisible to `ListTags` in org A.

- [ ] **Step 2: Run them to make sure they fail**
  Run: `go test ./internal/repo -run 'Tag' -v` → FAIL (undefined: `AddProjectTag`).

- [ ] **Step 3: Implement the five functions in `internal/repo/tags.go`** with `s.Tx`, following `internal/repo/projects.go`.

- [ ] **Step 4: Run them** → PASS.

- [ ] **Step 5: Commit** `feat(projects): store and look up project tags`

### Task 3: Tag filter on `/dashboard`

**Files:**
- Create: `internal/web/tagfilter.go`, `web/templates/components/tag_filter.html`
- Modify: `internal/web/home_dashboard.go:339-430` (`homeDashboardPage`), `internal/repo/dashboard.go:159` (`OrgDailyTrends`), `web/templates/pages/dashboard.html`, `web/static/css/app.css`
- Test: `internal/web/tagfilter_test.go`, `internal/repo/dashboard_test.go` (or the file holding `OrgDailyTrends` tests), `e2e/tests/tags.spec.ts`

**Interfaces:**
- Consumes: `repo.ListTags`, `repo.ProjectIDsWithAnyTag`.
- Produces:
  - `type tagChip struct { Name, Href string; Active bool }`
  - `type tagFilter struct { All tagChip; Chips []tagChip; Selected []repo.Tag }` — `Selected` empty means All.
  - `func buildTagFilter(path string, raw []string, tags []repo.Tag) tagFilter` — `raw` is every `tag` query value; matched to `tags` ignoring case; unknown names dropped; duplicates collapsed. Each chip's `Href` toggles that tag in the current selection (`path` plus the remaining `?tag=` values in `tags` order, or bare `path` when none remain). `All.Href == path`, `All.Active == (len(Selected) == 0)`.
  - `func (f tagFilter) projectFilter(ctx context.Context, s tenant.Scope) (map[uuid.UUID]bool, error)` — nil when All; otherwise the set from `ProjectIDsWithAnyTag`.
  - `OrgDailyTrends(ctx, s, combine, days, now, projectIDs []uuid.UUID)` — new trailing parameter; nil means every project; non-nil adds `AND p.id = ANY($n)` to `liveWhere`.
  - Template `tag_filter` taking a `tagFilter`, rendered only when the org has at least one tag.

- [ ] **Step 1: Write the failing tests**
  - `TestBuildTagFilter_UnknownNamesFallBackToAll`: `raw = ["nope"]`, tags `PROJECT, PRODUCT` → `Selected` empty, `All.Active`.
  - `TestBuildTagFilter_MatchesIgnoringCaseAndTogglesInTheURL`: `raw = ["project"]` → `Selected == [PROJECT]`; the `PROJECT` chip is `Active` with `Href == "/dashboard"`; the `PRODUCT` chip `Href == "/dashboard?tag=PROJECT&tag=PRODUCT"`.
  - `TestOrgDailyTrends_NarrowsToTheGivenProjects`: two projects with one daily fact each; passing one project's ID returns only its value; passing nil returns the combined value.

- [ ] **Step 2: Run them** → FAIL.

- [ ] **Step 3: Implement `buildTagFilter` and `projectFilter`; add the `projectIDs` parameter to `OrgDailyTrends` and update its callers.**

- [ ] **Step 4: Narrow `homeDashboardPage`.** After loading, when the filter is set keep only: `in.Components` whose `ProjectID` is in the set, `in.FailingBindings` likewise, `in.SevereIssues` whose `ProjectID` is in it; `in.ProjectCount` counts only matching projects; `OrgDailyTrends` gets the set's IDs; `ListQAProjectSummaries` and `CountMyQAItems` get the IDs (they already take `projectIDs`). `in.Connections` (org-wide) and QA workload/activity stay unfiltered. Render the `tag_filter` component above the dashboard columns, with the data passed through `homeDashboard` (add field `Filter tagFilter`).

- [ ] **Step 5: Run unit tests** `go test ./internal/web ./internal/repo` → PASS.

- [ ] **Step 6: E2E** in `e2e/tests/tags.spec.ts`, test `dashboard narrows to the chosen tags`: seed two projects each with one component and a failing/passing `component_metrics_current` row (pattern: `e2e/tests/home-dashboard.spec.ts`), tag one `PROJECT` and one `PRODUCT` by SQL. Assert: All shows both components on the health map; clicking chip `PROJECT` changes the URL to `?tag=PROJECT` and shows only its component; clicking `PRODUCT` too shows both again with both chips pressed (`aria-pressed="true"`); clicking All returns to `/dashboard`; visiting `/dashboard?tag=nope` shows both components with All pressed.
  Run: `make e2e` → PASS.

- [ ] **Step 7: Commit** `feat(web): filter the dashboard by project tag`

### Task 4: Folders out of the UI; `/projects` becomes a flat, tag-filtered list

**Files:**
- Modify: `internal/web/dashboard.go` (`dashboardPage`, drop `hasFolder`, `folderNav`), `internal/web/dashboardview.go` (`buildProjectDashboardViewModel` and the folder fields of `dashboardViewModel`), `internal/web/shell.go` (`projectTrail`, `componentTrail`, `metricTrail`, `projectKindTrail` lose their `folders` parameter; delete `projectFolderPath`), their callers in `project_page.go`, `component_page.go`, `metric_detail.go`, `project_metric.go`, `qa_access.go`; `internal/web/project_new.go` and `web/templates/pages/project_new.html` (no folder picker; `FolderID: nil`); `web/templates/pages/project.html` and `project_page.go` (no "Move to folder"); `internal/web/project_page.go:224` (after delete, redirect to `/projects`); `internal/web/flash.go` (drop `folder.*`, `project.moved`); `web/templates/pages/projects.html`, `web/templates/components/dashboard_block.html` (drop `folder_view_row`, selection, drag, "Move to folder"; rows show their tags); `web/static/js/lotus.js` (`lotusTree` → only the All/Failing filter state), `internal/web/server.go:160` (drop `registerFolderRoutes`)
- Delete: `internal/web/folders.go`, `internal/web/foldertree.go` (move `projectRow` and anything still used into `dashboardview.go` first), `web/templates/components/folder_dialogs.html`, `internal/web/folders_test.go`, `foldertree_test.go`, `folders_page_test.go`, `project_folder_test.go`, `folder_row_cloak_test.go`, `e2e/tests/folders.spec.ts`
- Keep: `internal/repo/folders.go` and its tests, and the `folder.*` permission keys in the matrix and seed (schema stays; spec §5.4 leaves dormant code to the builder — dormant is cheaper than a permission migration nobody needs).
- Test: `internal/web/projects_page_test.go`, `e2e/tests/tags.spec.ts`

**Interfaces:**
- Consumes: Task 3 `buildTagFilter`, `projectFilter`; Task 2 `ProjectTagsByProject`.
- Produces: `func buildProjectDashboardViewModel(subtitle string, projects []repo.Project, tags map[uuid.UUID][]repo.Tag, components []componentInfo, metrics map[uuid.UUID]repo.ComponentMetric) dashboardViewModel`; `dashboardRow.Tags []repo.Tag`; `dashboardViewModel.Filter tagFilter`. Folder routes `/folders*`, `/projects/move`, `/projects/:id/move` no longer exist (404).

- [ ] **Step 1: Write the failing tests**
  - `TestProjectsPage_IsFlatAndShowsTags` (Go, rendering through the app like the deleted `folders_page_test.go` did): org with folder `ITSD` holding p1 (tag `PROJECT`) and root project p2 → `GET /projects` lists both names at the top level, p1's row contains `PROJECT`, the body contains no `Move to folder`, `New folder` or `data-folder`.
  - `TestProjectsPage_OldFolderLinkStillRenders`: `GET /projects?folder=<ITSD id>` → 200 with both projects.
  - `TestProjectsPage_FiltersByTag`: `GET /projects?tag=PROJECT` lists p1 only.
  - `TestFolderRoutesAreGone`: `POST /folders`, `POST /projects/<id>/move` → 404.
  - `TestProjectBreadcrumbHasNoFolder`: project page trail is `[org, project]` for a project filed in a folder.

- [ ] **Step 2: Run them** → FAIL.

- [ ] **Step 3: Rewrite `dashboardPage`/`buildProjectDashboardViewModel` for the flat list** (rows sorted by name, tags from `ProjectTagsByProject`, project set narrowed by the filter; the All/Failing toggle keeps working on the narrowed rows; `?folder=` is ignored). Render `tag_filter` above the table.

- [ ] **Step 4: Remove every folder UI path listed under Files.**

- [ ] **Step 5: Run** `make lint && make test` → PASS.

- [ ] **Step 6: E2E** `projects list is flat and filters by tag` in `e2e/tests/tags.spec.ts`: seeded as in Step 1; assert both rows visible with tag pills; chip `PROJECT` hides p2; no element with text `Move to folder` or `New folder`; `/projects/new` has no `Folder` field and creating a project lands it in the list. Delete `e2e/tests/folders.spec.ts`.
  Run: `make e2e` → PASS.

- [ ] **Step 7: Commit** `refactor(web): replace the folder view with a flat project list` (body: folders stay in the database, only the UI stops using them; why tags replace them).

### Task 5: Editing a project's tags

**Files:**
- Create: `internal/web/project_tags.go`, `web/templates/components/project_tags.html`
- Modify: `internal/web/project_page.go` (pass `Tags []repo.Tag`, `AllTags []repo.Tag`, `CanTag bool`), `web/templates/pages/project.html` (render `project_tags` under the title), `internal/web/server.go` (call the new `registerProjectTagRoutes(app, d, requireSession, loadPrincipal)` next to `registerProjectPageDetailRoutes`)
- Test: `internal/web/project_tags_test.go`, `e2e/tests/tags.spec.ts`

**Interfaces:**
- Consumes: `repo.AddProjectTag`, `repo.RemoveProjectTag`, `repo.ListTags`.
- Produces routes, both `tenant.RequireTenant(), requireSession, loadPrincipal, authz.Require("project.tag")`:
  - `POST /projects/:id/tags` form field `name` → htmx swaps `#project-tags` with the re-rendered `project_tags` partial; without htmx, redirects to the project page. `ErrInvalidTag` → 422 with the message inside the partial.
  - `POST /projects/:id/tags/:tid/remove` → same swap.
  Each records an audit event: `project.tag.add` / `project.tag.remove`, target `project`, metadata `{"tag": "<name>"}`.

- [ ] **Step 1: Write the failing tests**
  - `TestProjectTags_OwnerAddsAndRemoves`: owner posts `name=xlsmart` → 200, partial contains `xlsmart`; remove → partial no longer contains it; two audit rows exist.
  - `TestProjectTags_MemberIsForbidden`: member posting to either route → 403; viewer sees tags on the project page but no add input and no remove buttons.
  - `TestProjectTags_InvalidNameIs422`: `name=` (blank) → 422 with `1–40 characters`.

- [ ] **Step 2: Run them** → FAIL.

- [ ] **Step 3: Implement the handlers and partial.** The partial shows each tag as a pill; for `CanTag`, a remove button per pill and a text input with a `<datalist>` of `AllTags` plus an "Add" button.

- [ ] **Step 4: Run** `go test ./internal/web` → PASS.

- [ ] **Step 5: E2E** `owner tags a project from its page`: owner opens `/projects/pay`, types `XLSMART`, presses Add → pill `XLSMART` appears; `/dashboard` now shows chip `XLSMART`; removing it from the project page removes the chip from `/dashboard`.
  Run: `make e2e` → PASS.

- [ ] **Step 6: Commit** `feat(projects): let owners and admins tag a project`
