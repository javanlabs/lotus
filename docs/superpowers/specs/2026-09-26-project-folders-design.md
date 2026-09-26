# Project folders

**Date:** 2026-09-26
**Status:** Approved (design), not yet planned or implemented

## 1. Context

An organization's projects are a flat list today. Javan, the organization
Lotus runs for, is really two divisions — Product (AlurKerja, AOS Online,
SyarQ) and IT Services (client work such as Linknet, Dignas, ANRI) — and IT
Services is further split by tribe lead. Nobody can see "how is IT Services
doing" or "how is Tribe lead A doing" without adding it up by hand.

The goal is not only to group projects but to give each group a number that
**moves the team to keep every project passing its Lotus score**. That goal
decided the rollup rule (§4) more than anything else.

## 2. Decisions

| Decision | Choice | Rejected alternatives |
|---|---|---|
| Concept | **Folders**: arrangement only | Workspaces, meaning a partition with its own members, connections and settings. Javan needs no separation between divisions, and the organization is already that boundary. |
| Tree ownership | **One shared tree per organization**; a project is in exactly one folder | Personal per-user views; tags |
| "Monitoring" a folder | **The `/projects` page becomes a collapsible tree with a rollup on every folder row** | A personal watchlist; alerts. Both remain possible later. |
| Storage | **A separate `folders` table plus a nullable `projects.folder_id`** | Nestable projects (blurs folder and project, and every project query must learn to skip folders); `ltree` materialized paths (costly moves, and the speed isn't needed at tens of folders) |
| Folder rollup | **Share of projects passing** | Average score (lets a healthy project hide a failing one, and is the easiest number to game); worst project (never rewards partial progress) |

Separation between divisions was explicitly asked about and ruled out:
everyone in the organization sees every folder. Per-folder access control
can be layered onto top-level folders later without restructuring the tree.

## 3. Data model

New table, tenant-scoped exactly like `projects`:

```sql
CREATE TABLE folders (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id      uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  parent_id   uuid NULL,               -- NULL = top level
  name        text NOT NULL,
  sort_order  integer NOT NULL DEFAULT 0,
  created_at  timestamptz NOT NULL DEFAULT now(),
  UNIQUE (id, org_id),
  FOREIGN KEY (parent_id, org_id) REFERENCES folders(id, org_id) ON DELETE RESTRICT
);
-- No two siblings share a name, including at the top level.
CREATE UNIQUE INDEX folders_sibling_name
  ON folders (org_id, parent_id, lower(name)) NULLS NOT DISTINCT;
-- RLS: the same forced tenant_isolation policy every tenant table carries.

ALTER TABLE projects ADD COLUMN folder_id uuid NULL;   -- NULL = at the root
ALTER TABLE projects ADD FOREIGN KEY (folder_id, org_id)
  REFERENCES folders(id, org_id) ON DELETE RESTRICT;
```

The composite `(id, org_id)` foreign keys follow the pattern `components`
already uses: a project can never reference another organization's folder,
even through a bug. `NULLS NOT DISTINCT` needs Postgres 15 or later. Dev runs
17, and production must be checked to be on 15+ before this ships.

The migration only adds. Existing projects get `folder_id = NULL` and render
at the root exactly as today.

### Rules (enforced in `internal/repo`)

- **No cycles.** A folder cannot be moved into itself or into any of its
  descendants. The check and the move run in one transaction under a
  per-organization advisory lock, so two concurrent moves that are each
  valid alone cannot together form a loop.
- **Depth cap of 8 levels.** A move or create that would put any folder
  deeper than 8 is refused. The cap is a constant, easy to raise.
- **Only empty folders can be deleted.** "Empty" counts archived projects too.
  Otherwise a folder would refuse to delete because of projects nobody can
  see, so the error names them.
- **Archived projects keep their `folder_id`.** Unarchiving returns a
  project to where it was.
- **Folders hold folders and projects only.** Components stay inside projects.
- **No folder page or slug in v1.** Folders exist to structure `/projects`.

## 4. Rollup: share of projects passing

A folder answers one question: **how many of my projects pass?**

- A project **passes** when its Lotus status is pass (green), meaning no
  integration fails. This is the status `repo.RollupProject` already
  produces and the project row already shows.
- A folder counts **every project at any depth** beneath it, not just its
  direct children. "IT Services: 1 / 3 passing" counts the real projects
  under both tribe leads. A folder never averages another folder's number.
- **Headline: "N / M passing"**, where M counts only projects with a known
  status. It turns green at 80% or more, otherwise red. This is the same
  threshold, and the same `repo.PassRate` semantics, as the org-level KPI
  cards.
- **Status dot:** red if any project beneath is failing, so a folder at 80%
  never looks all-clear.
- **Failing projects are named** in the row's subtext ("failing: Linknet 42,
  ANRI 55"), up to 3, then "+N more". The row says what to fix next.
- **Unmeasured projects** (unknown status) are left out of M but shown
  separately ("2 not measured"), so never binding a project can't be used to
  look better.
- **An empty folder, or one with nothing measured,** shows "–" and no color,
  never a false pass.
- **Per-kind columns** use the same rule per integration. A folder's
  SonarQube cell "2/3" means 2 of the 3 projects reporting SonarQube pass it.
- **Folders show no average score.** Project rows keep their 0–100 Lotus
  score unchanged.

The org-wide score card and KPI cards at the top of `/projects` are
**unchanged**. Moving projects between folders must never change the
headline number, or reorganizing would look like a quality change.

**Why share passing.** An average lets a great project hide a failing one,
and it rewards polishing an already-green project as much as fixing a red
one, which works against the goal. "Worst project" is honest but doesn't
move until the last failure is fixed, so teams stop trying. A share of
projects passing can't be offset, moves with every fix, and has a plain
target. SonarQube portfolios roll projects up the same way for their
Releasability rating (the share of projects passing the quality gate), as
do the service-maturity scorecards in OpsLevel and Cortex.

### Where it lives

A pure function in `internal/repo/rollup.go` beside `RollupProject`, which
takes the project rollups already computed for the page and returns the
per-folder aggregate (passing, failing, unknown counts, overall and per
kind, plus the failing project names). The `/projects` handler loads the
organization's folders in one query, builds the tree in memory, and
aggregates bottom-up, with no extra query per folder.

## 5. The `/projects` page

The flat project list becomes a tree. The score card and KPI cards above it
are untouched.

```
                               SCORE            SONARQUBE  TRIVY  SENTRY  GITLAB
▾ Product                      ● 3 / 3 passing   3/3        2/2    –       3/3
     3 projects · 22 components
     AlurKerja                   84             (project rows as today)
▾ IT Services                  ● 1 / 3 passing   2/3        1/3    …
     3 projects · failing: Linknet 42, ANRI 55
  ▾ Tribe lead A               ● 1 / 2 passing
       Linknet                   42 ●
       Dignas                    95
  ▸ Tribe lead B               ● 0 / 1 passing
  Unfiled project                …
```

- **Folder rows:** a chevron, the name, "N / M passing" plus a bar in the score
  column, the status dot, and subtext giving the size and the failing
  projects.
- **Column order** comes from `bindableKinds`, as everywhere (see AGENTS.md).
- **Order:** within each folder, subfolders first, then projects, each by
  `sort_order` then name. Root-level projects come after the top-level
  folders. Column sorting reorders siblings within their parent and never
  flattens the tree.
- **Collapse and expand** in Alpine. Each viewer's choice is kept in
  `localStorage`, every access wrapped in try/catch, so the page works
  without it. The default is fully expanded.
- **The "Failing" filter** shows failing projects plus the parent folders
  that contain them.
- **Alpine `:style` uses the object form** (AGENTS.md).

### Managing the tree (no drag-and-drop in v1)

- A **"New folder"** button next to "New project" opens a name field and a
  parent picker.
- The **folder row menu** has Rename, Move to…, and Delete. Delete is disabled
  with the reason shown when the folder isn't empty.
- **Projects** get "Move to…" in their row menu, and a folder picker on the
  New project form and in project settings.
- **The "Move to…" picker** lists full paths ("IT Services / Tribe lead A") and
  leaves out targets that would create a cycle or exceed the depth cap. The
  server enforces both regardless.
- **Viewers** see the tree with no management controls.

## 6. Permissions and routes

New permissions in `internal/authz/principal.go`, mirroring the project
permissions role for role:

| Permission | owner | admin | member | viewer |
|---|---|---|---|---|
| `folder.create` | ✓ | ✓ | ✓ | |
| `folder.update` (rename, move, reorder) | ✓ | ✓ | ✓ | |
| `folder.delete` | ✓ | ✓ | | |

Moving a project between folders is a project edit: `project.update`.

```
POST   /folders          folder.create   {name, parent_id?}
PATCH  /folders/:id      folder.update   {name?, parent_id?, sort_order?}
DELETE /folders/:id      folder.delete
PATCH  /projects/:id     project.update  gains {folder_id}  (null = move to root)
```

All routes carry `tenant.RequireTenant()`, the session, the principal and
`authz.Require(...)`, the same chain as the project routes.
`TestRouteCoverage` fails any route registered without it.

## 7. Errors

Repo sentinels map to 4xx responses with a message the UI shows verbatim:

| Case | Status | Message |
|---|---|---|
| Move into itself or a subfolder | 409 | Can't move a folder into itself. |
| Beyond the depth cap | 409 | Folders can nest at most 8 levels. |
| Delete a non-empty folder | 409 | 'Tribe lead A' still contains 2 projects (Linknet, Dignas — 1 archived). |
| Duplicate sibling name | 409 | There's already a folder called 'Product' here. |
| Folder or parent in another organization | 404 | (RLS hides the row, as for projects) |
| Empty name | 422 | Folder name is required. |

## 8. Audit

The following are recorded through `repo.RecordAuditEvent`: `folder.create`,
`folder.rename`, `folder.move` (from and to parent), `folder.delete`, and
`project.move` (from and to folder). When a folder's number changes, the log
shows who reorganized what.

## 9. Testing

- **Rollup unit tests:** nested counting at any depth; unknown projects left
  out of M but counted as not measured; an empty folder; all failing; the
  80% boundary (79% red, 80% green); per-kind shares; failing names capped
  at 3 with "+N more".
- **Repo tests** against Postgres (per-test schema, as existing repo tests
  do): create and move; cycle rejection (self, child, deep descendant); the
  depth cap on both create and move; refusing to delete a folder whose only
  project is archived; duplicate names at the root and inside a folder; RLS
  cross-organization isolation for folders and for `projects.folder_id`;
  two concurrent moves that would jointly form a cycle, where exactly one
  must fail.
- **View-model tests:** tree order; folder subtext; kind column order
  asserted against `bindableKinds`; the Failing filter keeping parent
  folders; the org score card unchanged when projects are moved between
  folders.
- **Route and permission tests:** each route's permission; a viewer refused
  on every write.
- **One Playwright e2e spec:** create "IT Services / Tribe lead A", move two
  projects in, see "N / 2 passing", collapse the folder, reload, and check it
  stays collapsed.

## 10. Out of scope (v1)

- Drag-and-drop reordering and moving
- Folder pages or URLs (`/folders/:id`)
- Per-folder access control
- A personal watchlist and alerts on folder changes
- `lotus seed-services` filing imported projects into folders by their
  `product:` field
