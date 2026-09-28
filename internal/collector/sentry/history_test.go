package sentry_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/collector"
	"github.com/uyab/lotus/internal/dbtest"
	"github.com/uyab/lotus/internal/repo"
	"github.com/uyab/lotus/internal/tenant"
)

func sentrySnap(componentID uuid.UUID, env, hash string, total int, issueIDs ...string) repo.NewSentrySnapshot {
	s := repo.NewSentrySnapshot{ComponentID: componentID, ContentHash: hash, Environment: env, ProjectSlug: "backend", TotalIssues: total}
	for _, id := range issueIDs {
		s.Issues = append(s.Issues, repo.NewSentryIssue{IssueID: id, Title: "issue " + id, EventCount: 1})
	}
	return s
}

// Sentry keeps its history like the other kinds: a sync whose content
// changed adds a snapshot beside the old one (whose issues stay), an
// identical one only marks the latest as seen again.
func TestSentryHistory_ChangedContentAppendsIdenticalTouches(t *testing.T) {
	db := dbtest.Open(t)
	orgID, componentID, bindingID := seedSentryFixture(t, db, "acme-history")
	s := tenant.FromRun(db, orgID)
	ctx := context.Background()
	replace := func(snaps ...repo.NewSentrySnapshot) {
		t.Helper()
		if err := repo.ReplaceSentrySnapshots(ctx, s, componentID, bindingID, collector.Headline{}, snaps); err != nil {
			t.Fatal(err)
		}
	}

	replace(sentrySnap(componentID, "production", "h1", 5, "a"))
	first, _ := repo.ListSentrySnapshotsForBinding(ctx, s, bindingID)
	replace(sentrySnap(componentID, "production", "h1", 5, "a")) // unchanged
	replace(sentrySnap(componentID, "production", "h2", 7, "b")) // changed

	var rows int
	withOrgTxSentry(t, db, orgID, func(tx *sqlx.Tx) {
		if err := tx.Get(&rows, `SELECT count(*) FROM sentry_snapshots WHERE binding_id = $1`, bindingID); err != nil {
			t.Fatal(err)
		}
	})
	if rows != 2 {
		t.Fatalf("snapshots kept = %d; want 2 (the unchanged sync only touched)", rows)
	}
	current, _ := repo.ListSentrySnapshotsForBinding(ctx, s, bindingID)
	if len(current) != 1 || current[0].TotalIssues != 7 {
		t.Fatalf("current = %+v; want the latest snapshot only", current)
	}
	old, _ := repo.ListSentryIssuesForSnapshot(ctx, s, first[0].ID)
	if len(old) != 1 || old[0].IssueID != "a" {
		t.Errorf("the superseded snapshot lost its issues: %+v", old)
	}
}

// The latest view shows the environments of the last sync only; a view
// as of an earlier moment shows what was current then, including an
// environment that has since gone.
func TestSentryHistory_LatestAndAsOf(t *testing.T) {
	db := dbtest.Open(t)
	orgID, componentID, bindingID := seedSentryFixture(t, db, "acme-asof")
	s := tenant.FromRun(db, orgID)
	ctx := context.Background()

	if err := repo.ReplaceSentrySnapshots(ctx, s, componentID, bindingID, collector.Headline{},
		[]repo.NewSentrySnapshot{sentrySnap(componentID, "production", "p1", 5), sentrySnap(componentID, "staging", "s1", 2)}); err != nil {
		t.Fatal(err)
	}
	between := time.Now()
	time.Sleep(20 * time.Millisecond)
	if err := repo.ReplaceSentrySnapshots(ctx, s, componentID, bindingID, collector.Headline{},
		[]repo.NewSentrySnapshot{sentrySnap(componentID, "production", "p2", 9)}); err != nil {
		t.Fatal(err)
	}

	envs := func(snaps []repo.SentrySnapshot) map[string]int {
		out := map[string]int{}
		for _, sn := range snaps {
			out[sn.Environment] = sn.TotalIssues
		}
		return out
	}
	latest, err := repo.SentrySnapshotsAt(ctx, s, bindingID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := envs(latest); len(got) != 1 || got["production"] != 9 {
		t.Errorf("latest = %v; want production=9 only", got)
	}
	then, err := repo.SentrySnapshotsAt(ctx, s, bindingID, &between)
	if err != nil {
		t.Fatal(err)
	}
	if got := envs(then); len(got) != 2 || got["production"] != 5 || got["staging"] != 2 {
		t.Errorf("as of the first sync = %v; want production=5, staging=2", got)
	}
	// The dates are calendar days in the database's timezone, so "today"
	// is the database's too — Go's local date differs near midnight.
	var today string
	if err := db.Get(&today, `SELECT to_char(now(), 'YYYY-MM-DD')`); err != nil {
		t.Fatal(err)
	}
	dates, err := repo.SentrySnapshotDatesForBinding(ctx, s, bindingID)
	if err != nil || len(dates) != 1 || dates[0] != today {
		t.Errorf("dates = %v, %v; want today", dates, err)
	}
}

// An import (WithObservedAt) records Sentry snapshots at their own time,
// never touches a later live snapshot, and leaves the latest view to the
// live sync.
func TestSentryHistory_ImportAtObservedTime(t *testing.T) {
	db := dbtest.Open(t)
	orgID, componentID, bindingID := seedSentryFixture(t, db, "acme-sentry-import")
	s := tenant.FromRun(db, orgID)
	ctx := context.Background()

	if err := repo.ReplaceSentrySnapshots(ctx, s, componentID, bindingID, collector.Headline{},
		[]repo.NewSentrySnapshot{sentrySnap(componentID, "production", "live", 3)}); err != nil {
		t.Fatal(err)
	}
	earlier := time.Now().Add(-3 * time.Hour).Truncate(time.Second)
	if err := repo.ReplaceSentrySnapshots(repo.WithObservedAt(ctx, earlier), s, componentID, bindingID, collector.Headline{},
		[]repo.NewSentrySnapshot{sentrySnap(componentID, "production", "live", 3, "x")}); err != nil {
		t.Fatal(err)
	}

	latest, _ := repo.SentrySnapshotsAt(ctx, s, bindingID, nil)
	if len(latest) != 1 || latest[0].CapturedAt.Equal(earlier) {
		t.Fatalf("latest = %+v; want the live snapshot", latest)
	}
	then := earlier.Add(time.Minute)
	old, _ := repo.SentrySnapshotsAt(ctx, s, bindingID, &then)
	if len(old) != 1 || !old[0].CapturedAt.Equal(earlier) || !old[0].CollectedAt.Equal(earlier) {
		t.Fatalf("as of the import = %+v; want the imported snapshot at %v", old, earlier)
	}
	issues, _ := repo.ListSentryIssuesForSnapshot(ctx, s, old[0].ID)
	if len(issues) != 1 || !issues[0].SnapshotCapturedAt.Equal(earlier) {
		t.Errorf("imported issues = %+v", issues)
	}
}

// An unchanged sync refreshes the latest snapshot's score, grade and load
// (they decay with time) without adding a row.
func TestSentryHistory_UnchangedSyncRefreshesTheScore(t *testing.T) {
	db := dbtest.Open(t)
	orgID, componentID, bindingID := seedSentryFixture(t, db, "acme-sentry-refresh")
	s := tenant.FromRun(db, orgID)
	ctx := context.Background()
	for _, score := range []int{91, 84} {
		snap := sentrySnap(componentID, "production", "same-issues", 1, "a")
		sc, grade, load := score, "A", float64(100-score)
		snap.Score, snap.Grade, snap.Load = &sc, &grade, &load
		if err := repo.ReplaceSentrySnapshots(ctx, s, componentID, bindingID, collector.Headline{}, []repo.NewSentrySnapshot{snap}); err != nil {
			t.Fatal(err)
		}
	}
	latest, _ := repo.SentrySnapshotsAt(ctx, s, bindingID, nil)
	if len(latest) != 1 || latest[0].Score == nil || *latest[0].Score != 84 || *latest[0].Load != 16 {
		t.Fatalf("latest = %+v; want one snapshot with the refreshed score 84", latest)
	}
}
