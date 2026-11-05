package repo_test

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/collector"
	"github.com/uyab/lotus/internal/dbtest"
	"github.com/uyab/lotus/internal/repo"
	"github.com/uyab/lotus/internal/tenant"
)

var statusTestWIB = time.FixedZone("WIB", 7*3600)

// todayWIB is today's date in Jakarta, the status page's day boundary.
func todayWIB() time.Time {
	now := time.Now().In(statusTestWIB)
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
}

func mustComponent(t *testing.T, s tenant.Scope, projectID uuid.UUID, name string) *repo.Component {
	t.Helper()
	c, err := repo.CreateComponent(context.Background(), s, repo.NewComponent{
		ProjectID: projectID, Slug: strings.ToLower(name), Name: name, Kind: "backend",
	})
	if err != nil {
		t.Fatalf("create component %q: %v", name, err)
	}
	return c
}

func mustUpsert(t *testing.T, s tenant.Scope, componentID uuid.UUID, kind string, h collector.Headline) {
	t.Helper()
	if err := repo.UpsertComponentMetric(context.Background(), s, componentID, kind, h); err != nil {
		t.Fatalf("upsert %s: %v", kind, err)
	}
}

// statusDays reads every recorded day of a project in the last year.
func statusDays(t *testing.T, s tenant.Scope, projectID uuid.UUID) []repo.ProjectStatusDay {
	t.Helper()
	today := todayWIB()
	days, err := repo.ProjectStatusDays(context.Background(), s, projectID, today.AddDate(-1, 0, 0), today)
	if err != nil {
		t.Fatalf("ProjectStatusDays: %v", err)
	}
	return days
}

func dayOf(t *testing.T, days []repo.ProjectStatusDay, day time.Time) repo.ProjectStatusDay {
	t.Helper()
	for _, d := range days {
		if d.Day.Format("2006-01-02") == day.Format("2006-01-02") {
			return d
		}
	}
	t.Fatalf("no row for %s in %+v", day.Format("2006-01-02"), days)
	return repo.ProjectStatusDay{}
}

func componentNames(d repo.ProjectStatusDay) []string {
	out := make([]string, len(d.Components))
	for i, c := range d.Components {
		out[i] = c.Name
	}
	return out
}

func inOrgTx(t *testing.T, s tenant.Scope, fn func(tx *sqlx.Tx) error) {
	t.Helper()
	if err := s.Tx(context.Background(), fn); err != nil {
		t.Fatal(err)
	}
}

func TestRecordProjectDay_WritesTodayFromTheProjection(t *testing.T) {
	t.Parallel()
	db := dbtest.Open(t)
	s := tagTestOrg(t, db, "acme")
	p := mustProject(t, s, "pay", nil)
	api := mustComponent(t, s, p.ID, "API")
	web := mustComponent(t, s, p.ID, "Web")

	score := 90
	mustUpsert(t, s, api.ID, "sonarqube", collector.Headline{Status: collector.StatusPass, Score: &score, Grade: "A"})
	findings := int64(3)
	mustUpsert(t, s, web.ID, "trivy", collector.Headline{Status: collector.StatusFail, CountValue: &findings, CountLabel: "finding"})

	metrics, err := repo.ListComponentMetricsForProject(context.Background(), s, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := repo.RollupProject(metrics)

	days := statusDays(t, s, p.ID)
	if len(days) != 1 {
		t.Fatalf("got %d rows, want one for today: %+v", len(days), days)
	}
	got := dayOf(t, days, todayWIB())
	// The project's status is read from its score: SonarQube's 90 is the
	// only score (Trivy reports none), so the project passes.
	if got.Status != "pass" || got.Status != string(want.Status) {
		t.Errorf("status = %q, want pass (rollup %q)", got.Status, want.Status)
	}
	if (got.Score == nil) != (want.Score == nil) || (got.Score != nil && *got.Score != *want.Score) {
		t.Errorf("score = %v, want the rollup's %v", got.Score, want.Score)
	}
	if names := componentNames(got); !slices.Equal(names, []string{"API", "Web"}) {
		t.Fatalf("components = %v, want [API Web]", names)
	}
	sonar := got.Components[0].Kinds["sonarqube"]
	if sonar.Status != "pass" || sonar.Grade != "A" || sonar.Score == nil || *sonar.Score != 90 {
		t.Errorf("API sonarqube = %+v, want pass A 90", sonar)
	}
	trivy := got.Components[1].Kinds["trivy"]
	if trivy.Status != "fail" || trivy.CountValue == nil || *trivy.CountValue != 3 || trivy.CountLabel != "finding" {
		t.Errorf("Web trivy = %+v, want fail 3 finding", trivy)
	}
}

func TestRecordProjectDay_StripsLinksAndSummaries(t *testing.T) {
	t.Parallel()
	db := dbtest.Open(t)
	s := tagTestOrg(t, db, "acme")
	p := mustProject(t, s, "pay", nil)
	c := mustComponent(t, s, p.ID, "API")
	score := 80
	mustUpsert(t, s, c.ID, "sonarqube", collector.Headline{
		Status: collector.StatusPass, Score: &score, Summary: "secret", DeepLink: "https://sonar.example/x",
	})

	var raw string
	inOrgTx(t, s, func(tx *sqlx.Tx) error {
		return tx.Get(&raw, `SELECT components::text FROM project_status_days WHERE project_id = $1`, p.ID)
	})
	for _, leak := range []string{"secret", "sonar.example", "deep_link", "summary", "updated_at"} {
		if strings.Contains(raw, leak) {
			t.Errorf("stored components %s contain %q", raw, leak)
		}
	}
	got := dayOf(t, statusDays(t, s, p.ID), todayWIB())
	kh := got.Components[0].Kinds["sonarqube"]
	if kh.Summary != "" || kh.DeepLink != "" || kh.UpdatedAt != nil {
		t.Errorf("decoded headline = %+v, want no summary, link or time", kh)
	}
}

func TestRecordProjectDay_PastDaysAreFixed(t *testing.T) {
	t.Parallel()
	db := dbtest.Open(t)
	s := tagTestOrg(t, db, "acme")
	ctx := context.Background()
	p := mustProject(t, s, "pay", nil)
	old := mustComponent(t, s, p.ID, "old")
	mustUpsert(t, s, old.ID, "gitlab", collector.Headline{Status: collector.StatusFail})

	yesterday := todayWIB().AddDate(0, 0, -1)
	const yesterdayJSON = `[{"name":"old","kinds":{"gitlab":{"status":"fail"}}}]`
	inOrgTx(t, s, func(tx *sqlx.Tx) error {
		_, err := tx.Exec(`INSERT INTO project_status_days (org_id, project_id, day, status, score, components)
			VALUES ($1, $2, $3::date, 'fail', NULL, $4::jsonb)`, s.OrgID(), p.ID, yesterday.Format("2006-01-02"), yesterdayJSON)
		return err
	})

	// Archiving the only component drops it from today at once.
	if err := repo.SetComponentArchived(ctx, s, old.ID, true); err != nil {
		t.Fatal(err)
	}
	days := statusDays(t, s, p.ID)
	today := dayOf(t, days, todayWIB())
	if today.Status != "unknown" || today.Score != nil || len(today.Components) != 0 {
		t.Fatalf("today after archiving = %+v, want unknown, no score, no components", today)
	}
	if today.Components == nil {
		t.Errorf("today's components decoded as nil, want an empty list")
	}
	past := dayOf(t, days, yesterday)
	if past.Status != "fail" || !slices.Equal(componentNames(past), []string{"old"}) {
		t.Fatalf("yesterday after archiving = %+v, want unchanged", past)
	}

	// Hard-deleting a component rewrites today and leaves yesterday alone.
	// (Only a component that never collected anything can be hard-deleted:
	// its projection and snapshot rows keep foreign keys to it.)
	gone := mustComponent(t, s, p.ID, "gone")
	if err := repo.HardDeleteComponent(ctx, s, gone.ID); err != nil {
		t.Fatal(err)
	}
	days = statusDays(t, s, p.ID)
	if today := dayOf(t, days, todayWIB()); today.Status != "unknown" || len(today.Components) != 0 {
		t.Fatalf("today after delete = %+v, want unknown with no components", today)
	}
	if past := dayOf(t, days, yesterday); past.Status != "fail" || !slices.Equal(componentNames(past), []string{"old"}) {
		t.Fatalf("yesterday after delete = %+v, want unchanged", past)
	}
}

func TestProjectStatusDays_IncludesTheSeedBeforeTheRange(t *testing.T) {
	t.Parallel()
	db := dbtest.Open(t)
	s := tagTestOrg(t, db, "acme")
	p := mustProject(t, s, "pay", nil)
	other := mustProject(t, s, "hr", nil)
	inOrgTx(t, s, func(tx *sqlx.Tx) error {
		for _, row := range []struct {
			project uuid.UUID
			day     string
		}{{p.ID, "2026-09-15"}, {p.ID, "2026-10-01"}, {p.ID, "2026-10-20"}, {p.ID, "2026-11-02"}, {other.ID, "2026-10-05"}} {
			if _, err := tx.Exec(`INSERT INTO project_status_days (org_id, project_id, day, status, components)
				VALUES ($1, $2, $3::date, 'pass', '[]')`, s.OrgID(), row.project, row.day); err != nil {
				return err
			}
		}
		return nil
	})

	from := time.Date(2026, 10, 10, 0, 0, 0, 0, statusTestWIB)
	to := time.Date(2026, 10, 31, 0, 0, 0, 0, statusTestWIB)
	days, err := repo.ProjectStatusDays(context.Background(), s, p.ID, from, to)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, d := range days {
		got = append(got, d.Day.Format("2006-01-02"))
	}
	if want := []string{"2026-10-01", "2026-10-20"}; !slices.Equal(got, want) {
		t.Fatalf("days = %v, want %v (the newest before the range, then the range)", got, want)
	}
}

func TestSetProjectStatusPage(t *testing.T) {
	t.Parallel()
	db := dbtest.Open(t)
	s := tagTestOrg(t, db, "acme")
	ctx := context.Background()
	p := mustProject(t, s, "pay", nil)
	if p.StatusPageEnabledAt != nil {
		t.Fatalf("new project has its status page on: %v", p.StatusPageEnabledAt)
	}
	if err := repo.SetProjectStatusPage(ctx, s, p.ID, true); err != nil {
		t.Fatal(err)
	}
	got, err := repo.ProjectByID(ctx, s, p.ID)
	if err != nil || got.StatusPageEnabledAt == nil {
		t.Fatalf("after turning on: %+v, %v", got, err)
	}
	if err := repo.SetProjectStatusPage(ctx, s, p.ID, false); err != nil {
		t.Fatal(err)
	}
	if got, _ := repo.ProjectByID(ctx, s, p.ID); got.StatusPageEnabledAt != nil {
		t.Fatalf("after turning off: %v", got.StatusPageEnabledAt)
	}
	if err := repo.SetProjectStatusPage(ctx, s, uuid.New(), true); err != repo.ErrNotFound {
		t.Fatalf("unknown project err = %v, want ErrNotFound", err)
	}
}
