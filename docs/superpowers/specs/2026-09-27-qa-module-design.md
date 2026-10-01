# QA module (QA Tools rebuilt inside Lotus)

**Date:** 2026-09-27
**Status:** Final (2026-09-27). Phases 1–3 of the plan were validated against the production dump.
**Plan:** `docs/superpowers/plans/2026-09-27-qa-module.md`

## 1. Context

QA Tools (Laravel API + React SPA, `qa.javan.id`) is Javan's test management
tool: 191 live projects, 57,738 live test cases, 1,003 test runs, 64,911 run
details, 42,931 results, 9,995 issues and 2.8 GB of attachments (production
dump of 2026-09-26). It is being rebuilt as a module of Lotus, so that test
management sits next to the engineering metrics of the same projects, under
the same login, organization, projects, folders and permissions.

This design comes from an analysis of the production data (dump restored as
`qa_tools_analysis`, see §9). Three facts drove it:

1. **The hierarchy is missing a reusable scenario.** Users already work as
   "scenario → plan → run, repeated", but QA Tools has nowhere to keep a
   scenario. 39% of runs contain ≥80% cases already run before; 136 runs
   repeat an earlier run's exact case set; 74% of UAT run cases were
   re-picked from earlier non-UAT runs. Regression runs share only 21% of
   their cases with the previous regression run, so every cycle is
   hand-assembled and coverage drifts.
2. **Phase and environment live in free text.** 476 run titles say
   "Regresi/Regression", 268 "Sprint", 147 "UAT". Releases are named
   "Sprint 1.1" (52×), "UAT", "Regresi - September". Test plans are used by
   5% of runs. No report can say "UAT pass rate".
3. **Many fields are formalities.** Defaults were never changed: 88% of test
   cases are `type=ACCEPTANCE`, 65% `priority=CRITICAL`, 99.5% of issues
   `category=BUG`, 86% `issue_type=FUNCTIONAL_BUGS`. Several columns are
   0–1% filled. §6 lists every dropped field and why.

Two further findings shape the permission model:

- Client UAT already happens, with client staff registered as ordinary
  testers (bach-project.com, linknet.co.id, panahmerah.id recorded 1,701
  UAT results). They can therefore edit test cases and see every project.
- The legacy API has two authentication flaws (`X-Public: True` opens every
  GET route anonymously; `X-Auth-Force-Email` lets any valid token act as
  any user). The rebuild must not carry a share-link mechanism of that
  shape.

## 2. Decisions

| Decision | Choice | Rejected alternatives |
|---|---|---|
| Where QA lives | Tenant-plane tables prefixed `qa_`, hanging off Lotus `projects`. No separate QA project entity. | A `qa_projects` table mapped to Lotus projects (two names for one thing; folders and members would have to be duplicated). |
| Test case library grouping | `qa_sections`: a per-project tree (max depth 8), replacing legacy modules. | Keeping "module" (the name collided with Taiga user stories and with Lotus components). |
| Steps | `qa_cases.steps jsonb` array of `{action, expected}`. | A `qa_steps` table (411k rows never referenced individually; snapshotting a case into a run becomes one column copy instead of a child-table copy). |
| Reusable scope | `qa_scenarios` with one level of `qa_scenario_groups`; a scenario has a phase. | Tags on cases (no ordering, no grouping, no reuse as a unit). Nested groups (not needed; one level covers "Day 1 / Day 2" and business flows). |
| Release | **Dropped as an entity.** A plan carries `build` (version under test). Legacy releases become plans. | Keeping releases (legacy data shows they were used as sprint/phase buckets with a date window, which is exactly a plan). |
| Plan | Required parent of every run. Has phase, environment, build, date window, and the scenarios it covers. | Optional plan (legacy: 95% of runs skipped it, which is why it carried no information). |
| Run | One execution cycle of a plan. `cycle` number and `retest_of` link retests to their origin. | Run per tester (legacy workaround, 48 same-day groups / 195 runs). |
| Run content | `qa_run_items` snapshot the case (title, precondition, steps, version) when the run is created. | Live reference only (legacy: results point at case text that was edited afterwards). |
| Assignment | Per item, with bulk assign and distribution on run creation (none / round-robin / by group). **Revised 2026-10-02:** a run also has an optional assignee, its lead tester and the default for its items: at creation it gets every item distribution left unassigned, and changing it moves only the items still held by the previous one (per-item assignments stay); the new assignee is emailed. | Run-level assignee as the only assignment (legacy default that 81% of runs never changed). It was dropped at first; the team asked for it back as the run's lead (qatools #143076). |
| Executor vs assignee | Both kept: `assignee_id` (who should) and `executed_by` (who recorded the latest result). | Assignee only (legacy: 14.4% of results were recorded by someone else, so the assignee was not the truth). |
| Issue fields | `severity` only (blocker … trivial), plus status, environment, found-in plan. | Priority + severity + type + category (defaults never changed; §6). |
| Issue history | One timeline, `qa_issue_events`: comments and status changes. | Separate comments and status logs (legacy stored the issue status on every comment as well). |
| Attachments | `qa_attachments` with exactly one of `result_id / issue_id / event_id`, bytes in a pluggable blob store (filesystem by default). | Polymorphic `relation_model` string (no foreign key possible). |
| Client UAT | New org role `guest`, scoped to projects listed in `project_members`; can execute, report issues, sign off. | Clients as members (see every project, edit cases); anonymous share links for execution. |
| Sharing | Signed, expiring, revocable read-only report links per plan (`qa_share_links`). | A global "public" header (legacy flaw). |
| Project visibility for members | Members see every project's QA (as they see every project's metrics). | Legacy per-project assignee lists for testers (kept only for guests). |
| Case identifiers | Per-project integer `key`, shown as `TC-<key>`; issues as `BUG-<key>`. | Legacy zero-padded strings `TC0001` / `D0001` (8 duplicate case codes and 1 duplicate issue code exist in live data). |

## 3. Hierarchy

```
Organization
└── Project (Lotus)                      ← folders, components, metrics already here
    ├── Section tree → Case              ← library; steps inside the case
    ├── Scenario (phase) → Group → Case  ← reusable scope, ordered
    ├── Plan (phase, environment, build, window) ── covers 1..n Scenarios
    │     ├── Run (cycle n, retest_of) → Run item (case snapshot, assignee, status)
    │     │                                   └── Result (history, attachments)
    │     └── Sign-off (per plan or per scenario)
    └── Issue (severity, status) ── linked to run items; timeline of events
```

A plan can be repeated from the same scenarios (e.g. monthly regression):
"Duplicate plan" copies phase, environment and scenarios into a new plan.

## 4. Data model

All tables: `org_id uuid NOT NULL REFERENCES organizations ON DELETE
CASCADE`, `UNIQUE (id, org_id)`, composite foreign keys `(x_id, org_id)`
to every tenant parent, RLS forced with the `NULLIF(current_setting(
'app.org_id', true), '')::uuid` policy used since migration 00026. User
references (`*_by`, `assignee_id`) are plain `REFERENCES users(id) ON
DELETE SET NULL` (control plane, no org).

| Table | Columns (besides id, org_id) | Notes |
|---|---|---|
| `project_members` | project_id, user_id, created_at | Only consulted for role `guest`. `UNIQUE (project_id, user_id)`. |
| `qa_sections` | project_id, parent_id NULL, name (1–200), sort_order, archived_at, created_at | Sibling names unique ignoring case (`NULLS NOT DISTINCT`). Depth ≤ 8. |
| `qa_cases` | project_id, section_id, key int, title (1–500), precondition text, notes text, steps jsonb (array), priority text NULL (`high/medium/low`), negative bool, version int, archived_at, created_by, created_at, updated_at | `UNIQUE (project_id, key)`. `version` +1 whenever title, precondition or steps change. |
| `qa_scenarios` | project_id, name (1–200), phase, description, archived_at, created_by, created_at, updated_at | phase ∈ `smoke, sit, uat, regression, other`. |
| `qa_scenario_groups` | scenario_id, name (1–200), position | Optional; items without a group sit in an unnamed first group. |
| `qa_scenario_items` | scenario_id, group_id NULL, case_id, position | `UNIQUE (scenario_id, case_id)`. |
| `qa_plans` | project_id, name (1–200), phase, environment text, build text, starts_on date NULL, ends_on date NULL, closed_at, created_by, created_at | `CHECK (ends_on >= starts_on)` when both set. |
| `qa_plan_scenarios` | plan_id, scenario_id | Composite PK. |
| `qa_runs` | plan_id, name (1–200), cycle int ≥ 1, retest_of NULL, notes text, assignee_id NULL, closed_at, created_by, created_at | `retest_of` references `qa_runs (id, org_id)` in the same plan (checked in repo). `assignee_id` (added 2026-10-02, migration 00031): the run's lead and its items' default, `ON DELETE SET NULL`. Notes written in Lotus are at most 2,000 characters (checked in repo; imported notes may be longer). |
| `qa_run_items` | run_id, case_id, group_name text, position, title, precondition, steps jsonb, case_version, assignee_id NULL, status, status_at NULL, executed_by NULL | status ∈ `untested, passed, failed, blocked, skipped`. `UNIQUE (run_id, case_id)`. |
| `qa_results` | item_id, status, comment text, created_by NULL, created_at | Append-only. Writing one updates the item's status/status_at/executed_by in the same transaction. A comment is at most 2,500 characters (checked in repo, 2026-10-02). Rows go only with their item: removing items from an open run, or deleting a run (open or closed), cascades to items, results, their files and issue links; issues stay. |
| `qa_issues` | project_id, key int, title (1–500), description, steps_to_reproduce, severity, status, environment text, plan_id NULL, section_id NULL, assignee_id NULL, reported_by NULL, source, created_at, updated_at | severity ∈ `blocker, critical, major, minor, trivial`; status ∈ `new, open, fixed, closed, invalid, duplicate, reopened`; source ∈ `internal, client`. `UNIQUE (project_id, key)`. |
| `qa_issue_links` | item_id, issue_id, created_at | Composite PK. |
| `qa_issue_events` | issue_id, kind, body, status_from NULL, status_to NULL, actor_id NULL, created_at | kind ∈ `comment, status`. |
| `qa_attachments` | result_id NULL, issue_id NULL, event_id NULL, file_name, content_type, size_bytes, storage_key, created_by NULL, created_at | `CHECK (num_nonnulls(result_id, issue_id, event_id) = 1)`. |
| `qa_signoffs` | plan_id, scenario_id NULL, decision, note, signed_by, signed_at | decision ∈ `accepted, accepted_with_notes, rejected`. |
| `qa_share_links` | plan_id, token_hash (unique), expires_at, revoked_at, created_by, created_at | Read-only plan report. |
| `qa_legacy_ids` | legacy_table, legacy_id uuid, new_id uuid, imported_at | `PRIMARY KEY (org_id, legacy_table, legacy_id)`. Import idempotency (and tracing a row back to its legacy id); droppable after decommission. No legacy-URL redirects (decided 2026-09-28). |

Per-project key allocation (`qa_cases.key`, `qa_issues.key`) takes
`pg_advisory_xact_lock(hashtext('qa_key:' || project_id))` and uses
`max(key)+1`, like the per-org lock folders use.

## 5. Permissions

New keys in `internal/authz` and the `role_permissions` seed:

| Permission | owner | admin | member | viewer | guest |
|---|---|---|---|---|---|
| `qa.read` | ✓ | ✓ | ✓ | ✓ | ✓ (own projects) |
| `qa.case.write` (sections, cases, scenarios, CSV import) | ✓ | ✓ | ✓ | | |
| `qa.plan.write` (plans, runs, assignment, close, retest) | ✓ | ✓ | ✓ | | |
| `qa.execute` (record results) | ✓ | ✓ | ✓ | | ✓ (own projects) |
| `qa.issue.write` (edit, status, assign) | ✓ | ✓ | ✓ | | |
| `qa.issue.report` (create, comment) | ✓ | ✓ | ✓ | | ✓ (own projects) |
| `qa.signoff` | ✓ | ✓ | | | ✓ (own projects) |
| `qa.share` | ✓ | ✓ | ✓ | | |
| `project_member.manage` | ✓ | ✓ | | | |

`guest` has **no** `dashboard.read`: Lotus metrics stay hidden from clients.
"Own projects" is enforced by `authz.RequireProjectAccess`, a middleware on
every `/projects/:id/qa…` route: for role `guest` the project must appear in
`project_members`, otherwise 404 (same anti-enumeration rule as
`CheckOwnership`). Guest project lists and "My work" filter through the same
table. Admins can invite a guest to specific projects in one step (existing
invitation flow + a project picker).

## 6. Fields dropped (and why)

Measured on the 2026-09-26 production dump. "Fill" = non-null share.

| Legacy field | Evidence | Decision |
|---|---|---|
| `test_cases.type` | 88% `ACCEPTANCE` (default) | Drop. The scenario phase carries what "type" tried to say. |
| `test_cases.priority` | 65% `CRITICAL` (default) | Keep as optional `high/medium/low`; import `CRITICAL` as NULL (indistinguishable from the default), others as-is. |
| `test_cases.tag` | 83% `POSITIVE`, 17% `NEGATIVE` | Keep as `negative bool` (the 17% were deliberate). |
| `test_cases.description` | 41% `-`, most of the rest Taiga links | Becomes `notes`; `-` imported as empty. |
| `test_cases.case_code` | Strings `TC0001`, 8 live duplicates | Integer `key`; duplicates renumbered. |
| `test_cases.order_id` | 12 distinct values | Drop; order by key within section. |
| `test_cases.pic_id`, `id_task_taiga`, `id_assigned_to_taiga`, `assigned_to_taiga` | 0.0% fill | Drop. |
| `steps.created_by/updated_by/created_at/updated_at` | Steps belong to the case | Drop; steps move into `qa_cases.steps`. |
| `modules.id_epicorus_taiga`, `deleted_at_taiga_sync`, `pic_id` | 1.2%, 0%, 0% | Drop (Taiga is decommissioned). |
| `projects.id_taiga` | 9.3%, Taiga gone | Drop. |
| `releases.*` | Used as sprint/phase buckets | Entity dropped; each becomes a plan (name, dates). |
| `test_plans.code`, `assignment_time`/`expiration_time` naming | 4 distinct codes | Drop code; dates become `starts_on/ends_on`. |
| `test_runs.result_id` | 0% | Drop. |
| `test_runs.assignee_id` | Default copied to every detail | Imported as `qa_runs.assignee_id` through the users map (since 2026-10-02; it was dropped before the run assignee came back). Items keep their own assignee. |
| `test_runs.version`, `browser` | 15%, 17%; free text | Appended to run `notes` on import ("Build: …", "Browser: …"); the plan's `build` is the field going forward. |
| `test_runs.is_archive` | 23 rows | Drop; `closed_at` replaces it. |
| `test_run_details.code` | 0% | Drop. |
| `test_run_details.is_archive` | per-row archive | Drop. |
| `test_run_details.created_by/updated_by/deleted_by` | Audit noise; results carry the actor | Drop. |
| `test_run_detail_issues.is_create` | Whether the issue was created from this detail | Drop. |
| `test_issues.issue_type` | 86% `FUNCTIONAL_BUGS` | Drop. |
| `test_issues.category` | 99.5% `BUG` | Drop. |
| `test_issues.priority` | Overlaps severity | Drop; `SHOW_STOPPER` maps to severity `blocker`. |
| `test_issues.start_date/end_date` | 22% | Drop. |
| `test_issues.test_run_id` | 0.5% | Drop; links go through run items. |
| `test_issues.is_archive` | 60 rows | Drop; archived issues are imported with their status. |
| `test_issues.code_issue` | Strings `D0001` | Integer `key`. |
| `comments.status` | Copy of the issue status | Drop; status changes are their own events. |
| `project_assignees.access` | 95% `FULL_ACCESS` | Drop; org role decides. Rows kept only for guests. |
| `users.achievement`, `avatar_user` | Unused | Drop (Lotus users have `avatar_url`). |
| `ak_activity_log`, `ak_notif_users` | Framework tables | Not migrated. |
| `sync_taigas` | Taiga | Not migrated. |
| `custom_fields`, `custom_options`, `project_custom_field` | 1 field, 2 options, 1 project | Not migrated. |
| `roles` (legacy) | 0 rows | Not migrated. |
| `personal_access_tokens` | MCP tokens | Not migrated; users create Lotus API tokens. |
| All `updated_by`, `deleted_by` | Audit trail moves to `audit_events` for sensitive actions | Drop. |
| Soft-delete `deleted_at` everywhere | | Deleted rows are not imported (§7), except cases/sections still referenced by imported rows, which come in archived. |

## 7. Legacy migration

`lotus import-qa` (read-only on the legacy database) imports one legacy
installation into one Lotus organization. It is idempotent: every created
row is recorded in `qa_legacy_ids`, so an interrupted import resumes and a
second run creates nothing twice. Status/enum mapping:

| Legacy | New |
|---|---|
| detail/result `PASSED` | `passed` |
| `FAILED` | `failed` |
| `BLOCKED`, `HOLD` | `blocked` |
| `NOT_APPLICABLE` | `skipped` |
| `UNTESTED`, `IN_PROGRESS`, `RETEST` | `untested` |
| issue `NEW/OPEN/FIXED/CLOSED/INVALID/DUPLICATE/REOPEN` | `new/open/fixed/closed/invalid/duplicate/reopened` |
| issue severity `CRITICAL/MAJOR/MINOR/TRIVIAL` | same, lower case; `priority=SHOW_STOPPER` overrides to `blocker` |
| user `ADMIN` / `TESTER` / `EXTERNAL` | membership `admin` / `member` / `guest` |
| `status_user=false` | membership `suspended` |

Rules:

- **Users** are matched on lower-cased email. A `--users-map` CSV
  (`legacy_email,canonical_email`) merges the 10 people with two accounts.
  Users with no activity since `--dormant-before` get a user row (for
  attribution) but no membership.
- **Projects** are matched through a `--projects-map` CSV
  (`legacy_project_id,lotus_project_slug`); unmapped live projects that hold
  QA data are created, filed in the folder given by `--folder` (default
  "QA Tools (imported)"). Deleted legacy projects and everything in them are
  skipped (34 live runs sit in deleted projects).
- **Sections** from modules, preserving the tree. Deleted modules come in
  archived only when a live case still sits in them (56 cases).
- **Cases** from live test cases, plus deleted cases still referenced by a
  live run detail (1,050 details), imported archived. Steps ordered by
  `position_step`. Keys parsed from `case_code`; missing or duplicate keys
  get the next free number; a renumbered case keeps its old code at the top
  of its notes (`Legacy code: TC-LIST-01`). On the 2026-09-26 dump this
  affects 289 cases, mostly codes like `TC-LIST-01` whose digits collide
  with `TC0001`.
- **Plans**: each live legacy test plan → plan; each live release → plan
  (name, dates, phase guessed from the name); one "Unplanned (imported)"
  plan per project for runs with neither (55 runs). A run with both a test
  plan and a release goes to the test plan. Phase is parsed from the
  plan/run name (`uat`, `sit`, `regres`→regression, `smoke`, else `other`).
- **Runs** from live runs; items from live details. Item snapshot = the
  case content at import time (legacy kept none). Item status = the
  legacy detail status (the value users saw), not the last result — 544
  details disagree with their last result and 11,953 non-untested details
  have no result at all.
- **Results** from live results on imported items. `executed_by` / `status_at`
  on the item = the latest imported result, else the detail's
  `updated_status_at`.
- **Issues** from live issues; `release_id` → the plan made from that
  release; module → section. Keys from `code_issue`, same renumbering rule
  (old code kept at the top of the description).
- **Text** is imported as written except: surrounding whitespace is trimmed,
  a lone `-` (the legacy placeholder, e.g. 37,211 step expectations) becomes
  empty, and names over 200 characters are cut with `…` (28 modules).
- **Issue events**: comments → `comment` events; `test_issue_logs` →
  `status` events; merged by time.
- **Attachments**: `minio` rows whose owner was imported; bytes copied from
  `--files-dir` (a mirror of the legacy bucket, `mc mirror`) into the blob
  store. Missing files are reported, not fatal. Attachments of releases (60)
  and user avatars (7) are not migrated: releases have no counterpart that
  holds files, and Lotus users have their own avatars.
- A JSON **report** lists per-table counts imported/skipped with reasons;
  `lotus import-qa verify` recomputes per-project totals on both sides, checks
  that the organization holds no QA data outside the imported projects, and
  exits non-zero on any mismatch. Concurrent runs are serialised per project,
  so two imports started by mistake cannot duplicate data.

## 8. Rollout

1. Build phases 1–3 (schema, blob store, importer); rehearse the import
   against the dump until `verify` is clean.
2. Build the UI phases; run QA Tools and Lotus QA side by side on the
   rehearsal data.
3. Cut-over day: set QA Tools read-only, final import, verify, switch
   ceo-monitor to the Lotus QA summary endpoint. Old `qa.javan.id` URLs
   are not redirected (decided 2026-09-28).
4. Keep the legacy database read-only for 90 days, then drop
   `qa_legacy_ids` and decommission.

## 9. Evidence

Numbers in this document come from `qa_tools_analysis` (local Postgres,
port 5416), restored from the production dump of 2026-09-26, analysed on
2026-09-27. Queries live in the session that produced this design; the view
`analysis.ev` in that database unifies activity events.

## 10. Out of scope

Requirements traceability to Taiga/GitLab issues, automation result ingest
(JUnit), custom fields, per-folder access control for members, email
notifications beyond invitations. Each can be layered on later without
changing the tables above.
