# Public Status Page Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A per-project public page at `/status/<project-slug>?date=YYYY-MM-DD` showing the project's verdict, a 30-day strip and its components for the chosen day, published only after an owner/admin turns it on.

**Architecture:** A new table `project_status_days` holds one row per project per WIB day: the project rollup plus each live component's display-safe headlines. It is rewritten for *today* inside the same transaction that changes the dashboard projection (`rollupProjectionTx`), and when a component is archived or deleted; past rows are never touched, so history is fixed. The public handler reads those rows (carrying the last one forward over days without a write) and renders a standalone page with no app shell. `projects.status_page_enabled_at` is the publish switch.

**Tech Stack:** Go, Fiber, sqlx, goose, html/template, Alpine.js, htmx, Tailwind v4, Playwright.

**Spec:** `docs/superpowers/specs/2026-11-03-tags-and-status-page-design.md` §2, §3, §4, §6, §7. Run after `2026-11-03-project-tags.md` (its migration is `00039`).

## Global Constraints

- URL: `/status/<project-slug>`, optional `?date=YYYY-MM-DD`. Slug, never a UUID or token.
- Day boundary: WIB, `Asia/Jakarta`. A day's value is the last result at end of that day, carried forward from earlier days.
- Default off. Only `owner` and `admin` can turn it on or off (permission `project.status_page`).
- Unknown slug, deleted project, page off: identical 404.
- Headers: `X-Robots-Tag: noindex, nofollow`, `Referrer-Policy: no-referrer`.
- Never shown on the page: vulnerability or issue lists, error messages, connection names, binding config, links into the app, people, tags, other projects, `KindHeadline.Summary`, `KindHeadline.DeepLink`.
- No-data day: grey "No data", never Passed. No backfill: days before launch are "No data".
- A carried-forward result never expires (user decision).
- History is fixed once recorded: deleting a component or binding never rewrites a past day.
- Rollup identical to the signed-in project page: `repo.RollupProject`.
- Kind columns in `bindableKinds` order: SonarQube, Trivy, Sentry, GitLab.
- Alpine `:style` object form only.
- Commits: atomic Conventional Commits passing `make lint` and `make test`, ending with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`; commit only when the user has asked for commits in this session.

## Review Focus

1. A selected date after the project's last recorded day (sync stopped): the verdict must carry the last row forward, not show No data, matching "never expires". Test in Task 3.
2. `?date=` garbage, a future date, or a date before retention: no 500. Garbage and future fall back to today; a date before the first row shows No data. Test in Task 3.
3. A project slug that exists in another organization (SaaS mode): must 404 on this host. Covered by RLS through `tenant.RequireTenant`; test in Task 3.
4. A kind headline carrying `deep_link`/`summary` (Sonar and Sentry write them): those must never reach the stored day row nor the HTML. Test in Task 1.
5. Archiving a project's only component: today's row must drop it at once (the project then reads No data for today), while yesterday's row still lists it. Test in Task 1.

---

### Task 1: Daily status table and recording it

**Files:**
- Create: `internal/db/migrations/00040_project_status_days.sql`, `internal/repo/status_days.go`
- Modify: `internal/repo/component_metrics.go` (`rollupProjectionTx` calls the recorder), `internal/repo/components.go` (`SetComponentArchived`, `HardDeleteComponent` call it), `internal/repo/projects.go` (`Project.StatusPageEnabledAt`, `projectCols`), `internal/authz/principal.go` (`project.status_page` for owner and admin), `internal/retention/retention.go` (`runOnce` default branch also calls `DeleteOldProjectStatusDays`)
- Test: `internal/repo/status_days_test.go`, `internal/retention/retention_test.go`

**Interfaces:**
- Produces:
  - Table `project_status_days (org_id, project_id, day date, status text, score integer NULL, components jsonb NOT NULL, updated_at timestamptz, PRIMARY KEY (project_id, day))`, composite FK `(project_id, org_id) → projects(id, org_id) ON DELETE CASCADE`, forced RLS and grants like `00026_folders.sql`. No FK to components (history outlives them).
  - Column `projects.status_page_enabled_at timestamptz NULL`.
  - Permission `project.status_page` seeded for owner and admin (with Down).
  - `type StatusDayComponent struct { Name string \`json:"name"\`; Kinds map[string]KindHeadline \`json:"kinds"\` }` — each `KindHeadline` stripped to `Status, Score, Grade, CountValue, CountLabel, StateLabel` (Summary, DeepLink, UpdatedAt cleared).
  - `type ProjectStatusDay struct { Day time.Time \`db:"day"\`; Status string \`db:"status"\`; Score *int \`db:"score"\`; Components []StatusDayComponent; UpdatedAt time.Time \`db:"updated_at"\` }` (components decoded from jsonb, ordered by name).
  - `func recordProjectDayTx(ctx context.Context, tx *sqlx.Tx, projectID uuid.UUID) error` — reads `component_metrics_current` joined to live (`archived_at IS NULL`) components of the project, computes `RollupProject`, upserts the row for `(now() AT TIME ZONE 'Asia/Jakarta')::date`. With no measured component left, it writes status `unknown`, score NULL, components `[]`.
  - `func ProjectStatusDays(ctx context.Context, s tenant.Scope, projectID uuid.UUID, from, to time.Time) ([]ProjectStatusDay, error)` — rows with `from <= day <= to`, plus the newest row before `from` if one exists (the carry-forward seed), ascending by day.
  - `func SetProjectStatusPage(ctx context.Context, s tenant.Scope, id uuid.UUID, on bool) error` — `now()` or NULL; `ErrNotFound` on no row.
  - `func DeleteOldProjectStatusDays(ctx context.Context, db *sqlx.DB, cutoff time.Time) error`.

- [ ] **Step 1: Write the failing tests**
  - `TestRecordProjectDay_WritesTodayFromTheProjection`: two components, `UpsertComponentMetric` sonar pass score 90 on one and trivy fail on the other → one row for today (WIB) with status `fail`, score equal to `RollupProject(...)`'s, two components by name.
  - `TestRecordProjectDay_StripsLinksAndSummaries`: headline with `DeepLink: "https://sonar.example/x"`, `Summary: "secret"` → the stored jsonb contains neither string.
  - `TestRecordProjectDay_PastDaysAreFixed`: insert yesterday's row by SQL listing component `old`; `SetComponentArchived(old, true)` → today's row omits `old` (and with `old` the only component, today is `unknown` with `[]`), yesterday's row unchanged; `HardDeleteComponent` on another component likewise leaves yesterday untouched.
  - `TestProjectStatusDays_IncludesTheSeedBeforeTheRange`: rows on Oct 1 and Oct 20; range Oct 10–Oct 31 → returns Oct 1 then Oct 20.
  - `TestDeleteOldProjectStatusDays` in retention tests: rows older than cutoff gone, newer kept.

- [ ] **Step 2: Run them** `go test ./internal/repo ./internal/retention -run 'ProjectDay|StatusDays' -v` → FAIL.

- [ ] **Step 3: Write the migration, then implement the repo functions and hook `recordProjectDayTx`** at the end of `rollupProjectionTx` (project id from the updated `component_metrics_current` row) and inside `SetComponentArchived` / `HardDeleteComponent` (read the component's `project_id` first).

- [ ] **Step 4: Add the permission to the Go matrix and the retention call.**

- [ ] **Step 5: Run** `make test` → PASS (including `TestRolePermissionsSeedMatchesTheGoMatrix`).

- [ ] **Step 6: Commit** `feat(metrics): record each project's status per day`

### Task 2: Publish switch on the project page

**Files:**
- Create: `internal/web/status_publish.go`, `web/templates/components/status_publish.html`
- Modify: `internal/web/project_page.go` (data: `StatusPage struct{ On bool; URL string; Can bool }`), `web/templates/pages/project.html`, `internal/web/server.go` (register `registerStatusPublishRoutes`), `internal/web/flash.go` (`project.status_page.on` "Public status page turned on", `project.status_page.off` "Public status page turned off")
- Test: `internal/web/status_publish_test.go`, `e2e/tests/status-page.spec.ts`

**Interfaces:**
- Consumes: `repo.SetProjectStatusPage`.
- Produces: `POST /projects/:id/status-page` with form `enabled=1|0`, chain `tenant.RequireTenant(), requireSession, loadPrincipal, authz.Require("project.status_page")`; records audit `project.status_page.enable` / `.disable` (target `project`); redirects to the project page with the flash. Public URL shown is `d.Cfg.TenantURL(org.Subdomain) + "/status/" + slug`, as `qa_share.go:256` builds share links.

- [ ] **Step 1: Write the failing tests**: owner turns it on → `status_page_enabled_at` set, audit row, project page shows `/status/<slug>` and a copy button; member posting → 403; viewer sees no switch.
- [ ] **Step 2: Run** → FAIL.
- [ ] **Step 3: Implement handler, partial and wiring.**
- [ ] **Step 4: Run** `go test ./internal/web` → PASS.
- [ ] **Step 5: Commit** `feat(projects): let owners and admins publish a status page`

### Task 3: The public status page

**Files:**
- Create: `internal/web/status_page.go`, `internal/web/status_view.go`, `web/templates/pages/status.html`
- Modify: `internal/web/server.go` (register `registerStatusPageRoutes` beside `registerQAShareRoutes`, outside the session chain), `web/static/css/app.css` (status page styles, `@media print`)
- Test: `internal/web/status_view_test.go`, `internal/web/status_page_test.go`, `e2e/tests/status-page.spec.ts`

**Interfaces:**
- Consumes: `repo.ProjectBySlug`, `repo.ProjectStatusDays`, `bindableKinds`, `kindLabels`, `homeMapCellLabel` (the headline text the health map already uses).
- Produces:
  - Route `GET /status/:slug` with `tenant.RequireTenant(), authz.Public("public status page: answers only for a project whose owner/admin published it")`.
  - `var wib = time.FixedZone("WIB", 7*3600)`
  - `func parseStatusDate(raw string, now time.Time) time.Time` — WIB midnight of `raw` (`2006-01-02`), or of today when raw is empty, malformed or after today.
  - `type statusCell struct { Day time.Time; Tone string /* "pass" | "fail" | "none" */; Label string /* "28 Oct 2026 · Failed" */; Href string; Selected bool }`
  - `type statusView struct { OrgName, ProjectName, Date, DateValue string; Score string; Verdict, Tone string; Cells []statusCell; StripStart, StripEnd string; Columns []string; Rows []statusRow; UpdatedAt string }` with `type statusRow struct { Name string; Cells []statusKindCell }`, `type statusKindCell struct { Text, Tone string }` (`"–"`/`none` when the kind is absent).
  - `func buildStatusView(orgName string, p *repo.Project, days []repo.ProjectStatusDay, selected time.Time) statusView` — 30 cells ending at `selected`, each the newest row with `day <= cell day` (none before the first row → `none`); status `pass` → Passed, `fail` → Failed, `unknown` or no row → "No data" with tone `none` and no score; `Href` is `/status/<slug>?date=<day>`.

- [ ] **Step 1: Write the failing tests**
  - `TestParseStatusDate`: `""`, `"nope"`, `"2099-01-01"` → today WIB; `"2026-10-28"` → that day.
  - `TestBuildStatusView_CarriesTheLastDayForward`: rows Oct 20 fail and Oct 25 pass, selected Nov 2 → verdict Passed; cells Oct 4–19 `none`, Oct 20–24 `fail`, Oct 25–Nov 2 `pass`; last cell `Selected`.
  - `TestBuildStatusView_NoDataIsNeverPassed`: no rows → verdict "No data", tone `none`, empty score, every cell `none`.
  - `TestBuildStatusView_ColumnsFollowBindableKinds`: `Columns` equals the labels of `bindableKinds` in order; a component with only `gitlab` has `–` in the first three cells.
  - `TestStatusPage_Answers404Alike` (through the app): unknown slug, soft-deleted project, published-then-off project, project published in another org → all 404 with the same body.
  - `TestStatusPage_PublishedRendersWithoutLogin`: no cookie, published project → 200, headers `X-Robots-Tag: noindex, nofollow` and `Referrer-Policy: no-referrer`, body contains the project name and `Failed`, and contains none of `deep_link`, `href="/projects`, the connection name, the tag name.
  - `TestStatusPage_ArchivedProjectStillAnswers`: published, then archived → 200.
  - `route_coverage_test.go` passes with the new route.
- [ ] **Step 2: Run** → FAIL.
- [ ] **Step 3: Implement `parseStatusDate`, `buildStatusView`, the handler and `pages/status.html`** (Layout A from the approved mockup: header with org line "PT Javan Cipta Solusi · Quality report" built from the org name, project name, a native `<input type="date" max=today>` that submits `?date=`; verdict block; strip of 30 links with `aria-label` = `Label`; component table; footer "Updated <UpdatedAt> · Powered by Lotus"). No app shell, no links into the app. Print styles keep the colors (`print-color-adjust: exact`).
- [ ] **Step 4: Run** `make lint && make test` → PASS.
- [ ] **Step 5: E2E** `e2e/tests/status-page.spec.ts`:
  - `status page is off until an owner publishes it`: `/status/pay` 404 in a fresh context; owner turns it on from `/projects/pay`; a cookie-less context gets 200 and sees `Payments`; turning it off makes it 404 again.
  - `picking a date shows that day`: seed `project_status_days` rows by SQL (Oct 20 fail score 62, Oct 25 pass score 84); open `/status/pay?date=2026-10-22` → verdict `Failed` and `62`; click the Oct 25 cell → URL `?date=2026-10-25`, verdict `Passed`; fill the date input with `2026-09-01` → `No data`.
  Run: `make e2e` → PASS.
- [ ] **Step 6: Commit** `feat(web): add the public project status page`

## Deviation from the spec

Spec §6.2 asks for a warning when an owner/admin edits the slug of a project whose status page is on. The UI has no slug-editing form; slugs change only through `PATCH /projects/:id` (JSON API). No warning is built; the old link simply 404s, as the spec already accepts.
