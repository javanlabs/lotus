package repo_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/dbtest"
	"github.com/uyab/lotus/internal/repo"
	"github.com/uyab/lotus/internal/tenant"
)

func withOrgTxNotes(t *testing.T, db *sqlx.DB, orgID uuid.UUID, fn func(tx *sqlx.Tx)) {
	t.Helper()
	tx, err := db.Beginx()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback() //nolint:errcheck
	if _, err := tx.Exec(`SELECT set_config('app.org_id', $1, true)`, orgID.String()); err != nil {
		t.Fatalf("set app.org_id: %v", err)
	}
	fn(tx)
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

func seedTrivyBindingForNotes(t *testing.T, db *sqlx.DB, subdomain string) (orgID, bindingID, userID uuid.UUID) {
	t.Helper()
	if err := db.Get(&orgID, `INSERT INTO organizations (subdomain, name) VALUES ($1, $1) RETURNING id`, subdomain); err != nil {
		t.Fatalf("insert org: %v", err)
	}
	if err := db.Get(&userID, `INSERT INTO users (email, email_verified_at) VALUES ($1, now()) RETURNING id`, subdomain+"@example.com"); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	withOrgTxNotes(t, db, orgID, func(tx *sqlx.Tx) {
		var projectID, componentID, connID uuid.UUID
		if err := tx.Get(&projectID, `INSERT INTO projects (org_id, slug, name) VALUES ($1, 'p', 'P') RETURNING id`, orgID); err != nil {
			t.Fatalf("insert project: %v", err)
		}
		if err := tx.Get(&componentID, `INSERT INTO components (org_id, project_id, slug, name, kind) VALUES ($1, $2, 'c', 'C', 'backend') RETURNING id`, orgID, projectID); err != nil {
			t.Fatalf("insert component: %v", err)
		}
		if err := tx.Get(&connID, `INSERT INTO integration_connections (org_id, provider, name, base_url) VALUES ($1, 'harbor_trivy', 'harbor', 'https://harbor.example.com') RETURNING id`, orgID); err != nil {
			t.Fatalf("insert connection: %v", err)
		}
		if err := tx.Get(&bindingID, `
			INSERT INTO metric_bindings (org_id, component_id, connection_id, kind, external_key)
			VALUES ($1, $2, $3, 'trivy', 'proj/app') RETURNING id`, orgID, componentID, connID); err != nil {
			t.Fatalf("insert binding: %v", err)
		}
	})
	return orgID, bindingID, userID
}

func TestUpsertTrivyFindingNote_SecondCallOnSameKeyEditsNotDuplicates(t *testing.T) {
	db := dbtest.Open(t)
	orgID, bindingID, userID := seedTrivyBindingForNotes(t, db, "acme")
	scope := tenant.FromRun(db, orgID)

	id1, err := repo.UpsertTrivyFindingNote(context.Background(), scope, repo.NewTrivyFindingNote{
		BindingID: bindingID, CVEID: "CVE-2026-1", Package: "openssl",
		Status: "false_positive", Comment: "doesn't apply, static build", AuthorUserID: &userID,
	})
	if err != nil {
		t.Fatalf("upsert 1: %v", err)
	}

	id2, err := repo.UpsertTrivyFindingNote(context.Background(), scope, repo.NewTrivyFindingNote{
		BindingID: bindingID, CVEID: "CVE-2026-1", Package: "openssl",
		Status: "wont_fix", Comment: "accepted risk", AuthorUserID: &userID,
	})
	if err != nil {
		t.Fatalf("upsert 2: %v", err)
	}
	if id1 != id2 {
		t.Fatalf("expected the same row id on a re-annotation of the same (binding,cve,package), got %s vs %s", id1, id2)
	}

	notes, err := repo.ListTrivyFindingNotes(context.Background(), scope, bindingID)
	if err != nil {
		t.Fatalf("ListTrivyFindingNotes: %v", err)
	}
	if len(notes) != 1 {
		t.Fatalf("got %d notes, want 1 (edit, not duplicate)", len(notes))
	}
	if notes[0].Status != "wont_fix" || notes[0].Comment != "accepted risk" {
		t.Fatalf("note not updated: %+v", notes[0])
	}
}

func TestListTrivyFindingNotes_ScopedToBinding(t *testing.T) {
	db := dbtest.Open(t)
	orgID, bindingA, _ := seedTrivyBindingForNotes(t, db, "acme")
	scope := tenant.FromRun(db, orgID)

	_, err := repo.UpsertTrivyFindingNote(context.Background(), scope, repo.NewTrivyFindingNote{
		BindingID: bindingA, CVEID: "CVE-2026-1", Package: "openssl", Comment: "note A",
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}

	notes, err := repo.ListTrivyFindingNotes(context.Background(), scope, uuid.New())
	if err != nil {
		t.Fatalf("ListTrivyFindingNotes: %v", err)
	}
	if len(notes) != 0 {
		t.Fatalf("got %d notes for an unrelated binding, want 0", len(notes))
	}
}

func TestDeleteTrivyFindingNote(t *testing.T) {
	db := dbtest.Open(t)
	orgID, bindingID, _ := seedTrivyBindingForNotes(t, db, "acme")
	scope := tenant.FromRun(db, orgID)

	id, err := repo.UpsertTrivyFindingNote(context.Background(), scope, repo.NewTrivyFindingNote{
		BindingID: bindingID, CVEID: "CVE-2026-1", Package: "openssl", Comment: "temp",
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := repo.DeleteTrivyFindingNote(context.Background(), scope, id); err != nil {
		t.Fatalf("delete: %v", err)
	}
	notes, err := repo.ListTrivyFindingNotes(context.Background(), scope, bindingID)
	if err != nil {
		t.Fatalf("ListTrivyFindingNotes: %v", err)
	}
	if len(notes) != 0 {
		t.Fatalf("got %d notes after delete, want 0", len(notes))
	}
}
