# QA Tools → Lotus import runbook

How to move the legacy QA Tools data into one Lotus organization with
`lotus import-qa`. Design: `docs/superpowers/specs/2026-09-27-qa-module-design.md`
§7–§8. Plan: `docs/superpowers/plans/2026-09-27-qa-module.md`, Task 19.

The importer never writes to the legacy database: it opens it with
`default_transaction_read_only=on`. Each legacy project is imported in one
transaction whose last write is its done marker, so a project is either fully
present or absent, and a re-run skips finished projects.

## 1. Inputs

| Input | Where it comes from |
|---|---|
| `--legacy-dsn` | A read-only role on the legacy database, e.g. the existing `ceomonitor` read-only user on `50.50.50.137:5432/qa_tools_dbs`. |
| `--org` | Subdomain of the target Lotus organization (`javan`). |
| `--users-map` | `docs/qa-import/users-map.csv`, header `legacy_email,canonical_email`. Merges people who have a personal and a `@javan.co.id` account. Every row is reviewed by hand: the same name is not proof of the same person. |
| `--projects-map` (optional) | CSV `legacy_project_id,lotus_project_slug` for legacy projects that already exist in Lotus under another slug. Unmapped projects are created (slug from the title) in the `--folder` folder. |
| `--files-dir` | A mirror of the legacy MinIO bucket: `mc mirror <legacy-alias>/<bucket> /srv/qa-minio-mirror`. Without it attachments are skipped and reported as "files not requested". |
| `LOTUS_BLOB_DIR` | Where the copied attachments land. Persistent, backed-up disk. |

Defaults worth knowing: `--dormant-before` is today − 1 year (people with no
activity since then get a user row for attribution but no membership);
`--close-idle-before` is today − 30 days (plans with no activity since then are
imported closed).

## 2. Rehearsal

Run against a fresh production dump restored locally, into a scratch Lotus
database (never the shared dev database):

```bash
psql "postgres://postgres@localhost:5418/postgres?sslmode=disable" -c "CREATE DATABASE lotus_qa_rehearsal"
export DATABASE_URL='postgres://postgres@localhost:5418/lotus_qa_rehearsal?sslmode=disable'
export MIGRATE_DATABASE_URL=$DATABASE_URL
go build -o /tmp/lotus ./cmd/lotus
/tmp/lotus migrate up
/tmp/lotus init --org-name=Javan --owner-email=<your email> --slug=javan
LEGACY='postgres://postgres@localhost:5416/qa_tools_analysis?sslmode=disable'

# 1. dry run: everything is written, then rolled back
/tmp/lotus import-qa --legacy-dsn="$LEGACY" --org=javan \
  --users-map=docs/qa-import/users-map.csv --dry-run --report=/tmp/qa-dry.json
# 2. real import + verify
/tmp/lotus import-qa --legacy-dsn="$LEGACY" --org=javan \
  --users-map=docs/qa-import/users-map.csv --report=/tmp/qa-real.json
/tmp/lotus import-qa verify --legacy-dsn="$LEGACY" --org=javan
```

3. Kill-and-resume: drop and recreate the scratch database, start the import
   in the background, `kill -KILL <pid>` after about 20 s (a scripted SIGINT
   to a background job is ignored), run it again, then `verify`. Compare row
   counts with the clean run.

### Measured on the 2026-09-26 dump (2026-09-27, no users map, no files)

| Step | Result |
|---|---|
| Dry run | 80 s, exit 0 |
| Real import | 83 s, exit 0 |
| `verify` | 6.5 s, `verify: ok` |
| Re-run | < 1 s; 169 "already imported", 22 "no QA data", nothing written |
| Kill at 20 s, then resume | 31 projects done and none partial when killed; resume imported the other 138 in 60 s; `verify: ok`; every row count identical to the clean run |

| Report entry | Imported | Skipped |
|---|---|---|
| projects | 169 | 22 without QA data |
| users / memberships | 191 / 72 | memberships: 119 dormant |
| project_members (guests) | 4 | — |
| sections | 8,867 | 937 deleted and empty |
| cases | 57,896 | — |
| plans (of which closed as idle) | 646 (582) | — |
| runs / items / results | 969 / 54,630 / 40,937 | results: 489 not a result |
| issues / issue events / issue links | 8,866 / 17,550 / 5,329 | links: 169 item or issue not imported |
| attachments | 0 | 12,734 files not requested |
| warnings | 289 renumbered cases, 0 renumbered issues, 0 flattened sections | — |

A dry run does not count `project_members`: guest users do not exist yet
while it runs. The real run reports 4.

Codes such as `TC-LIST-01` and `TC-SEARCH-01` share trailing digits with
`TC0001`, so 289 cases get a new number. Each keeps its old code at the top of
its notes (`Legacy code: TC-LIST-01`), which stays searchable.

With the users map, users drop by about 10 and memberships change
accordingly. Copying 2.8 GB of attachments adds to the import time: measure
it in the rehearsal with `--files-dir` and add it to the read-only window
below.

**Before cut-over, rehearse once more with `--files-dir`.** The 2026-09
rehearsal skipped every file, so the MinIO path format, missing-file handling
and the time to copy 2.8 GB have not been exercised on real data, and
`verify` does not count attachments or issue links. Check the report by hand:
`attachments` must show no "files not requested", and imported + "file
missing" + "owner not imported" must equal the legacy file count
(12,734 in the 2026-09 dump). Look into every "file missing" warning before
going further.

The report of the rehearsal import is kept as evidence in
`docs/qa-import/rehearsal-2026-09.json`.

## 3. Cut-over

1. Announce the read-only window (import time from the rehearsal + attachment
   copy + 30 minutes of checks).
2. Put QA Tools in read-only mode: its maintenance flag, or revoke write
   grants from its application role.
3. Final `mc mirror` of the bucket to `--files-dir`.
4. `lotus import-qa` without `--dry-run`, with the users map, projects map and
   files dir; keep `--report`.
5. `lotus import-qa verify`. It must print `verify: ok`.
6. Spot-check 3 projects in the UI: a large regression project, a project
   with client UAT, and one with attachments.
7. Switch ceo-monitor to the Lotus summary endpoint (Phase 8).
8. Enable the legacy URL redirects (Phase 8).
9. Announce.

If the import stops with an error, fix the cause and run the same command
again. Finished projects are skipped.

## 4. Rollback

Before the announcement, and only if the import is unusable:

1. Delete the imported organization's QA rows in dependency order
   (`qa_issue_links`, `qa_issue_events`, `qa_attachments`, `qa_issues`,
   `qa_results`, `qa_run_items`, `qa_runs`, `qa_share_links`, `qa_signoffs`,
   `qa_plan_scenarios`, `qa_plans`, `qa_scenario_items`,
   `qa_scenario_groups`, `qa_scenarios`, `qa_cases`, `qa_sections`,
   `qa_legacy_ids`, `project_members`) with `WHERE org_id = '<org id>'`.
   Then delete the projects created in the import folder, the folder itself,
   and the blob directory `qa/<org id>/legacy/`.
2. Re-open QA Tools for writes and announce.

## 5. After

- Keep the legacy database read-only for 90 days.
- Then drop `qa_legacy_ids` (a migration, together with the Phase 8 legacy
  redirects) and decommission QA Tools.
