# Project tags and public status page

**Date:** 2026-11-03
**Status:** Approved (design), not yet planned or implemented

## 1. Context

Folders (spec `2026-09-26-project-folders-design.md`) gave projects a tree.
In practice the tree is one level deep: production has two folders, `ITSD`
(25 projects) and `PRODUCT` (1 project, AlurKerja). Opening folders to reach
a project is friction with nothing gained, and a project can only sit in one
place.

Two changes, asked for together:

1. **Projects become a flat list, grouped by tags instead of folders.** The
   folder concept disappears from the UI. The database keeps it untouched.
2. **A public status page per project**, at a URL a person can type, used to
   show a client that a Javan project is of high quality.

They are independent and can ship in either order. Tags first is the natural
order because it is smaller and touches the screens the status page toggle
will sit on.

## 2. Who it is for, and why

**Tags** are for the Javan team reading the dashboard: narrowing the fleet to
the projects they care about.

**The status page** is a report to the client, proving the project is
healthy. Two ways it gets used:

- The client opens the link directly.
- A project manager or tribe lead picks a date and sends the page (or a
  screenshot of it) to the client by hand as the weekly quality report.
  Lotus does not send anything.

The page is **transparent**: a failing project shows as failing. "Red means
Javan has to fix it first." There is no hiding, softening or curating of
results.

## 3. Goals, non-goals, anti-goals

**Goals**

- No folder appears anywhere in the UI; every project is reachable from one
  flat list.
- A dashboard filter: **All**, or one or more tags.
- Owner and admin can tag projects and turn a project's status page on or off.
- A status page at `/status/<project-slug>` showing the overall verdict for a
  chosen date.

**Non-goals**

- Dropping or migrating the folder tables. `folders` and `projects.folder_id`
  stay exactly as they are; they are only no longer read or written by the UI.
- Sending reports (email, scheduled PDF). The weekly report is manual.
- A per-date table on the status page. The weekly report is a snapshot of one
  chosen day, not every day of the week.
- Per-client logins or client accounts.

**Anti-goals** (would make this a failure even if it works)

- A status page that is public before anyone chose to publish it.
- A status page that leaks internal detail to a client: vulnerability lists,
  error messages, connection names, people, other projects.
- A day with no data that reads as Passed.
- A status page that reads green while the project is failing.

## 4. Decisions

| Decision | Choice |
|---|---|
| Folder removal | UI only. DB schema and data untouched. |
| Project list | Flat. |
| Grouping | Tags. A project can carry any number of tags. |
| Tags in use at launch | Exactly two: `PROJECT` and `PRODUCT`. Client-name tags (XLSMART, Linknet, JMRB, …) are expected later and added by hand. |
| Initial tagging in production | Ships with the feature: every project in folder `ITSD` gets `PROJECT`, every project in folder `PRODUCT` gets `PRODUCT`. Done as a data migration, so it lands in production with the deploy (user approved applying it to production). |
| Who manages tags | `owner` and `admin`. |
| Who publishes a status page | `owner` and `admin`. |
| Status page default | **Off.** Public only after an owner/admin turns it on for that project. |
| Status page URL | `/status/<project-slug>`, optionally `?date=YYYY-MM-DD`. Slug, never a UUID or random token. |
| Status page layout | "Layout A — status strip" (mockup approved in brainstorming): verdict, 30-day strip, component table. |
| Day boundary | WIB (Asia/Jakarta). |

## 5. Tags

### 5.1 Model

New tenant-scoped tables, RLS like every tenant table:

- `tags`: id, org_id, name, created_at. Name unique per organization,
  case-insensitively.
- `project_tags`: project_id, tag_id, org_id. Composite `(id, org_id)`
  foreign keys as `components` and `folders` already use, so a project can
  never carry another organization's tag.

Tag names are shown as written (`PROJECT`, `PRODUCT`).

### 5.2 Managing tags

On the project page (where project name and description are edited today),
owner/admin see the project's tags and can add or remove them. Adding offers
existing tags and allows typing a new one, which creates it. Members, viewers
and guests see the tags but cannot change them.

A tag no project uses any more may linger; cleaning those up is not required
for launch (builder's call whether to delete orphans automatically).

### 5.3 Dashboard filter

At the top of `/dashboard`: **All** plus one chip per tag. Choosing tags
narrows the whole dashboard (health, health map, trends, where to start) to
projects carrying **any** of the chosen tags. A project normally belongs to
one client, so "all of the chosen tags" would almost always be empty. All is
the default and clears the selection.

The selection lives in the URL (for example `?tag=PROJECT&tag=PRODUCT`) so a
filtered view can be bookmarked and shared within the team.

### 5.4 Removing folders from the UI

Everything that shows, creates, moves, renames or deletes folders goes: the
folder tree in the sidebar, folder dialogs, the folder picker when creating a
project, folder rows and breadcrumbs on `/projects` and on project,
component and metric pages, and folder-specific flash messages. Folder-only
routes are removed (or return 404).

New projects are created with `folder_id` NULL (the column is already
nullable).

`/projects` becomes a plain flat list of every project, with the same
**All** / tag chips as the dashboard (same any-of semantics, same URL
parameter) and each row showing its tags (design call). Drag-to-move, bulk
"Move to folder" and folder rows go.

Existing folder tests are removed or rewritten for the flat list. The folder
repo code can stay if something else still uses it; otherwise the builder
decides whether to delete it now or leave it dormant.

## 6. Public status page

### 6.1 Turning it on

On the project page, owner/admin see a "Public status page" switch, off by
default, plus the public URL with a copy button once it is on. Turning it off
makes the URL answer 404 immediately. The switch state is stored on the
project (a new nullable column or a small table: builder's call).

### 6.2 URL and access

- `GET /status/<project-slug>` and `GET /status/<project-slug>?date=YYYY-MM-DD`.
- No session. Registered with `authz.Public(...)` and `tenant.RequireTenant()`
  like `/s/qa/:token` in `internal/web/qa_share.go`.
- Unknown slug, deleted project, or status page off: **404**, identical in all
  three cases so a guessed slug reveals nothing.
- A slug is guessable by design (the user asked for a typeable URL). The
  protection is that only published projects answer.
- `X-Robots-Tag: noindex` so search engines don't list client projects.
  Caching may be short-lived; builder's call.
- An invalid or future `date` falls back to today; a date before the first
  available data shows that day as "No data".
- An archived project whose status page is on keeps answering, showing its
  recorded history (design call): a finished project's report stays
  available to the client.
- Renaming a project's slug breaks the old link (it answers 404, like any
  unpublished slug); there is no redirect (design call). When an owner/admin
  edits the slug of a project whose status page is on, the form warns that
  links already sent to the client will stop working.

### 6.3 What it shows (Layout A)

From top to bottom:

1. **Header:** organization name ("PT Javan Cipta Solusi · Quality report"),
   project name, and a date picker showing the selected date (default today,
   WIB).
2. **Verdict:** the project's overall score and **Passed** or **Failed** for
   the selected date. If there is no data for that date, "No data" in grey,
   never Passed.
3. **30-day strip:** the 30 days ending on the selected date, one cell per
   day: pass color, fail color, or grey for no data. The selected day is
   outlined. Clicking a cell selects that date (navigates to its `?date=`).
   Each cell has a text label/tooltip (date and status) so it isn't color
   alone.
4. **Components on this date:** one row per component of the project, one
   column per kind in the fixed order **SonarQube, Trivy, Sentry, GitLab**
   (derived from `bindableKinds`). Each cell shows the kind's headline value
   (grade, count or score) colored by its status, or "–" if that kind isn't
   bound.
5. **Footer:** when the data was last updated, and "Powered by Lotus".

Nothing else. Specifically **not** shown: vulnerability or issue lists,
Sentry error messages, connection names, binding configuration, links into
the authenticated app, people, tags, other projects.

The page must look right when printed or screenshotted, because that is how
the weekly report reaches the client.

### 6.4 What a day's status means

- A component kind's status on day D is the **last known result at the end of
  day D (WIB)**, i.e. the latest sync on or before 23:59:59 WIB that day,
  carried forward from earlier days if nothing new was collected that day.
- **A carried-forward result never expires** (user decision, "samakan dulu"):
  a binding that is turned off, frozen or failing keeps its last result on
  the status page, exactly as `component_metrics_current` keeps it for the
  signed-in pages. The status page does not apply its own staleness cutoff.
  This is a known gap against the anti-goal "a day with no data that reads
  as Passed" (a binding stuck for a month shows its last result for that
  month); it was accepted for now to keep the two views identical and may be
  revisited.
- Component status and project verdict roll up exactly as today:
  `RollupStatus` (any fail → fail) and `RollupScore` (average of non-nil
  scores) in `internal/repo/rollup.go`. The status page must never disagree
  with what the authenticated project page would have shown on that day.
- A day before the project had any data, or beyond retention
  (`organizations.retention_days`, 365 in production), is "No data".
- **History is fixed once recorded** (design call, from "transparent"): each
  day's component and project verdict is stored when that day is recorded
  and never recomputed. Deleting a component or binding later does not turn
  a past red day green, so a report already sent to a client reads the same
  when reopened. The component table for a past day lists the components as
  they were that day.

### 6.5 Where daily history comes from

Today only `component_metrics_current` holds the computed status and score,
and only for *now*. The snapshot tables (`sonar_snapshots`,
`trivy_snapshots`, `gitlab_snapshots`, `sentry_snapshots`) keep raw values per
sync, but not the status or score each sync produced, and that status depends
on binding config at the time (e.g. Trivy's fail-on threshold).

So the status page needs a per-day record of each component kind's headline
(status, score, display value), recorded from launch onward.

**No backfill** (user decision). Days before launch are "No data" (grey) on
every status page; the 30-day strip fills in over the first month. Past days
are never recomputed from snapshots, because they would be scored with
today's rules and binding settings and could disagree with what Lotus showed
on that day.

## 7. Constraints

- AGENTS.md: fixed kind order from `bindableKinds`; Alpine `:style` object
  form; atomic Conventional Commits straight to `main`; `make lint` and
  `make test` pass at every commit.
- E2E tests for every user-visible behavior: filter, tag editing, folder
  absence, status page on/off/404, date selection, no-data day.
- Production runs Postgres 16.
- The status page goes through the existing route-coverage test
  (`internal/web/route_coverage_test.go`) as a deliberately public route.

## 8. Left to the builder

- Storage shape for the publish switch and the daily history.
- Exact permission keys for "manage tags" and "publish status page".
- Orphan tag cleanup.
- Whether dormant folder code is deleted or left.
- Cache headers on the status page.
