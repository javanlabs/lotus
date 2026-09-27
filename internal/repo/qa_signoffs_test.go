package repo_test

import (
	"context"
	"strings"
	"testing"

	"github.com/uyab/lotus/internal/repo"
)

func TestQASignoffs_LatestPerScopeAndValidation(t *testing.T) {
	f := newRunFixture(t, "acme-signoff")
	ctx := context.Background()
	sign := func(scenario *repo.QAScenario, decision, note string) error {
		in := repo.QASignoffInput{Decision: decision, Note: note}
		if scenario != nil {
			in.ScenarioID = &scenario.ID
		}
		_, err := repo.RecordQASignoff(ctx, f.s, f.plan.ID, in, nil)
		return err
	}
	for _, step := range []struct {
		scenario       *repo.QAScenario
		decision, note string
	}{
		{f.uat, "rejected", "Login fails on Safari"},
		{f.uat, "accepted", ""},
		{nil, "accepted_with_notes", "Minor copy issues remain"},
	} {
		if err := sign(step.scenario, step.decision, step.note); err != nil {
			t.Fatalf("sign %s: %v", step.decision, err)
		}
	}
	latest, err := repo.ListLatestQASignoffs(ctx, f.s, f.plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, so := range latest {
		scope := "plan"
		if so.ScenarioID != nil {
			scope = so.ScenarioID.String()
		}
		got = append(got, scope+"="+so.Decision)
	}
	want := []string{"plan=accepted_with_notes", f.uat.ID.String() + "=accepted"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("latest = %v; want %v", got, want)
	}

	// A note is required unless the decision is a plain accept; a scenario
	// must be in the plan; the decision must be one of three.
	other, _ := repo.CreateQAScenario(ctx, f.s, f.projectID, "Not in plan", "uat", "", nil)
	for name, err := range map[string]error{
		"rejected without note": sign(f.uat, "rejected", "  "),
		"with notes, no note":   sign(nil, "accepted_with_notes", ""),
		"scenario not in plan":  sign(other, "accepted", ""),
		"unknown decision":      sign(nil, "approved", ""),
		"note over 4000 runes":  sign(nil, "rejected", strings.Repeat("é", 4001)),
	} {
		if err != repo.ErrInvalidInput {
			t.Errorf("%s: %v; want ErrInvalidInput", name, err)
		}
	}
}
