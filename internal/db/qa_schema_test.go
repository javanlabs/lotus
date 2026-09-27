package db_test

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/dbtest"
)

var qaTables = []string{
	"qa_sections", "qa_cases", "qa_scenarios", "qa_scenario_groups", "qa_scenario_items",
	"qa_plans", "qa_plan_scenarios", "qa_runs", "qa_run_items", "qa_results",
	"qa_issues", "qa_issue_links", "qa_issue_events", "qa_attachments",
	"qa_signoffs", "qa_share_links", "qa_legacy_ids",
}

func TestQASchema_EveryTableForcesRLS(t *testing.T) {
	db := dbtest.Open(t)
	for _, table := range qaTables {
		var enabled, forced bool
		if err := db.QueryRow(`SELECT relrowsecurity, relforcerowsecurity FROM pg_class WHERE relname = $1`, table).
			Scan(&enabled, &forced); err != nil {
			t.Fatalf("%s: %v", table, err)
		}
		if !enabled || !forced {
			t.Errorf("%s: rls enabled=%v forced=%v; want both", table, enabled, forced)
		}
	}
}

// inOrg runs fn in a transaction bound to orgID, as tenant.Scope does.
func inOrg(t *testing.T, db *sqlx.DB, orgID uuid.UUID, fn func(tx *sqlx.Tx) error) error {
	t.Helper()
	tx, err := db.Beginx()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`SELECT set_config('app.org_id', $1, true)`, orgID.String()); err != nil {
		t.Fatal(err)
	}
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func TestQASchema_ChildCannotPointAcrossOrganizations(t *testing.T) {
	db := dbtest.Open(t)
	var orgA, orgB, projectA, projectB uuid.UUID
	db.Get(&orgA, `INSERT INTO organizations (subdomain, name) VALUES ('qa-a','A') RETURNING id`)
	db.Get(&orgB, `INSERT INTO organizations (subdomain, name) VALUES ('qa-b','B') RETURNING id`)
	inOrg(t, db, orgA, func(tx *sqlx.Tx) error {
		return tx.Get(&projectA, `INSERT INTO projects (org_id, slug, name) VALUES ($1,'p','P') RETURNING id`, orgA)
	})
	inOrg(t, db, orgB, func(tx *sqlx.Tx) error {
		return tx.Get(&projectB, `INSERT INTO projects (org_id, slug, name) VALUES ($1,'p','P') RETURNING id`, orgB)
	})
	// A section in org B that names org A's project is refused by the composite FK.
	err := inOrg(t, db, orgB, func(tx *sqlx.Tx) error {
		_, err := tx.Exec(`INSERT INTO qa_sections (org_id, project_id, name) VALUES ($1, $2, 'Login')`, orgB, projectA)
		return err
	})
	if err == nil || !strings.Contains(err.Error(), "foreign key") {
		t.Fatalf("cross-org section insert err = %v; want a foreign key violation", err)
	}
}

func TestQASchema_AttachmentHasExactlyOneOwner(t *testing.T) {
	db := dbtest.Open(t)
	var orgID uuid.UUID
	db.Get(&orgID, `INSERT INTO organizations (subdomain, name) VALUES ('qa-att','A') RETURNING id`)
	err := inOrg(t, db, orgID, func(tx *sqlx.Tx) error {
		_, err := tx.Exec(`INSERT INTO qa_attachments (org_id, file_name, content_type, size_bytes, storage_key)
			VALUES ($1, 'a.png', 'image/png', 1, 'k')`, orgID)
		return err
	})
	if err == nil || !strings.Contains(err.Error(), "qa_attachments_one_owner") {
		t.Fatalf("ownerless attachment err = %v; want qa_attachments_one_owner", err)
	}
}

func TestQASchema_StatusChecks(t *testing.T) {
	db := dbtest.Open(t)
	var orgID, projectID, sectionID, caseID, planID, runID uuid.UUID
	db.Get(&orgID, `INSERT INTO organizations (subdomain, name) VALUES ('qa-chk','A') RETURNING id`)
	err := inOrg(t, db, orgID, func(tx *sqlx.Tx) error {
		tx.Get(&projectID, `INSERT INTO projects (org_id, slug, name) VALUES ($1,'p','P') RETURNING id`, orgID)
		tx.Get(&sectionID, `INSERT INTO qa_sections (org_id, project_id, name) VALUES ($1,$2,'S') RETURNING id`, orgID, projectID)
		tx.Get(&caseID, `INSERT INTO qa_cases (org_id, project_id, section_id, key, title) VALUES ($1,$2,$3,1,'T') RETURNING id`, orgID, projectID, sectionID)
		tx.Get(&planID, `INSERT INTO qa_plans (org_id, project_id, name, phase) VALUES ($1,$2,'P','uat') RETURNING id`, orgID, projectID)
		tx.Get(&runID, `INSERT INTO qa_runs (org_id, plan_id, name) VALUES ($1,$2,'R') RETURNING id`, orgID, planID)
		_, err := tx.Exec(`INSERT INTO qa_run_items (org_id, run_id, case_id, title, status) VALUES ($1,$2,$3,'T','in_progress')`, orgID, runID, caseID)
		return err
	})
	if err == nil || !strings.Contains(err.Error(), "check") {
		t.Fatalf("bad status err = %v; want a check violation", err)
	}
}

// Deleting a project (repo.HardDeleteProject) or a whole organization must
// still work once QA rows hang off it: every chain cascades, and the NO
// ACTION references between QA tables are only checked at statement end.
func TestQASchema_ProjectAndOrgDeletionCascadeThroughQA(t *testing.T) {
	db := dbtest.Open(t)
	var orgID, projectID, sectionID, caseID, planID, runID, itemID, issueID uuid.UUID
	db.Get(&orgID, `INSERT INTO organizations (subdomain, name) VALUES ('qa-del','A') RETURNING id`)
	seed := func() error {
		return inOrg(t, db, orgID, func(tx *sqlx.Tx) error {
			tx.Get(&projectID, `INSERT INTO projects (org_id, slug, name) VALUES ($1, 'p-'||gen_random_uuid(), 'P') RETURNING id`, orgID)
			tx.Get(&sectionID, `INSERT INTO qa_sections (org_id, project_id, name) VALUES ($1,$2,'S') RETURNING id`, orgID, projectID)
			tx.Get(&caseID, `INSERT INTO qa_cases (org_id, project_id, section_id, key, title) VALUES ($1,$2,$3,1,'T') RETURNING id`, orgID, projectID, sectionID)
			tx.Get(&planID, `INSERT INTO qa_plans (org_id, project_id, name, phase) VALUES ($1,$2,'P','uat') RETURNING id`, orgID, projectID)
			tx.Get(&runID, `INSERT INTO qa_runs (org_id, plan_id, name) VALUES ($1,$2,'R') RETURNING id`, orgID, planID)
			tx.Get(&itemID, `INSERT INTO qa_run_items (org_id, run_id, case_id, title) VALUES ($1,$2,$3,'T') RETURNING id`, orgID, runID, caseID)
			tx.Get(&issueID, `INSERT INTO qa_issues (org_id, project_id, key, title, severity, plan_id) VALUES ($1,$2,1,'I','major',$3) RETURNING id`, orgID, projectID, planID)
			_, err := tx.Exec(`INSERT INTO qa_issue_links (org_id, item_id, issue_id) VALUES ($1,$2,$3)`, orgID, itemID, issueID)
			return err
		})
	}
	if err := seed(); err != nil {
		t.Fatal(err)
	}
	if err := inOrg(t, db, orgID, func(tx *sqlx.Tx) error {
		_, err := tx.Exec(`DELETE FROM projects WHERE id = $1`, projectID)
		return err
	}); err != nil {
		t.Fatalf("delete project with QA data: %v", err)
	}
	if err := seed(); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM organizations WHERE id = $1`, orgID); err != nil {
		t.Fatalf("delete organization with QA data: %v", err)
	}
}

// The scenario side of the same problem: scenario items -> cases, plan
// scenarios and sign-offs -> scenarios.
func TestQASchema_ScenarioChainsCascade(t *testing.T) {
	db := dbtest.Open(t)
	var orgID, projectID, sectionID, caseID, planID, scenarioID, groupID uuid.UUID
	db.Get(&orgID, `INSERT INTO organizations (subdomain, name) VALUES ('qa-del2','A') RETURNING id`)
	seed := func() error {
		return inOrg(t, db, orgID, func(tx *sqlx.Tx) error {
			tx.Get(&projectID, `INSERT INTO projects (org_id, slug, name) VALUES ($1, 'p-'||gen_random_uuid(), 'P') RETURNING id`, orgID)
			tx.Get(&sectionID, `INSERT INTO qa_sections (org_id, project_id, name) VALUES ($1,$2,'S') RETURNING id`, orgID, projectID)
			tx.Get(&caseID, `INSERT INTO qa_cases (org_id, project_id, section_id, key, title) VALUES ($1,$2,$3,1,'T') RETURNING id`, orgID, projectID, sectionID)
			tx.Get(&planID, `INSERT INTO qa_plans (org_id, project_id, name, phase) VALUES ($1,$2,'P','uat') RETURNING id`, orgID, projectID)
			tx.Get(&scenarioID, `INSERT INTO qa_scenarios (org_id, project_id, name, phase) VALUES ($1,$2,'Sc','uat') RETURNING id`, orgID, projectID)
			tx.Get(&groupID, `INSERT INTO qa_scenario_groups (org_id, scenario_id, name) VALUES ($1,$2,'G') RETURNING id`, orgID, scenarioID)
			if _, err := tx.Exec(`INSERT INTO qa_scenario_items (org_id, scenario_id, group_id, case_id) VALUES ($1,$2,$3,$4)`, orgID, scenarioID, groupID, caseID); err != nil {
				return err
			}
			if _, err := tx.Exec(`INSERT INTO qa_plan_scenarios (org_id, plan_id, scenario_id) VALUES ($1,$2,$3)`, orgID, planID, scenarioID); err != nil {
				return err
			}
			_, err := tx.Exec(`INSERT INTO qa_signoffs (org_id, plan_id, scenario_id, decision) VALUES ($1,$2,$3,'accepted')`, orgID, planID, scenarioID)
			return err
		})
	}
	if err := seed(); err != nil {
		t.Fatal(err)
	}
	if err := inOrg(t, db, orgID, func(tx *sqlx.Tx) error {
		_, err := tx.Exec(`DELETE FROM projects WHERE id = $1`, projectID)
		return err
	}); err != nil {
		t.Fatalf("delete project with scenario data: %v", err)
	}
	if err := seed(); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM organizations WHERE id = $1`, orgID); err != nil {
		t.Fatalf("delete organization with scenario data: %v", err)
	}
}
