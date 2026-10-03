package web

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/repo"
	"github.com/uyab/lotus/internal/tenant"
)

// projectPathOf is a project's page URL as the app links it — by slug —
// for tests that hold only the project's id.
func projectPathOf(t *testing.T, db *sqlx.DB, orgID, id uuid.UUID) string {
	t.Helper()
	p, err := repo.ProjectByID(context.Background(), tenant.FromRun(db, orgID), id)
	if err != nil {
		t.Fatalf("project %s: %v", id, err)
	}
	return projectURL(p)
}

// componentPathOf is componentPathOf for a component.
func componentPathOf(t *testing.T, db *sqlx.DB, orgID, id uuid.UUID) string {
	t.Helper()
	s := tenant.FromRun(db, orgID)
	comp, err := repo.ComponentByID(context.Background(), s, id)
	if err != nil {
		t.Fatalf("component %s: %v", id, err)
	}
	p, err := repo.ProjectByID(context.Background(), s, comp.ProjectID)
	if err != nil {
		t.Fatalf("project %s: %v", comp.ProjectID, err)
	}
	return componentURL(p, comp)
}

// metricPathOf is a binding's metric page URL, by slugs. The ids may be
// uuids or their strings, as the fixtures hand them out.
func metricPathOf(t *testing.T, db *sqlx.DB, orgID uuid.UUID, componentID, bindingID any) string {
	t.Helper()
	return componentPathOf(t, db, orgID, uuid.MustParse(fmt.Sprint(componentID))) + "/metrics/" + fmt.Sprint(bindingID)
}

// projectPath is projectPathOf in this environment's organization.
func (e qaEnv) projectPath(t *testing.T, id uuid.UUID) string {
	t.Helper()
	return projectPathOf(t, e.db, e.orgID, id)
}
