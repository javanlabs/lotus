package sentry

import (
	"testing"
	"time"
)

// The score, grade and load decay with time even when not one issue
// changed, so they stay out of the content hash: a sync of the same issues
// is "unchanged" and adds no snapshot (ReplaceSentrySnapshots refreshes
// them on the latest one instead).
func TestEnvironmentContentHash_IgnoresTheDecayingScore(t *testing.T) {
	now := time.Now()
	score, grade, load := 91, "A", 1.25
	score2, grade2, load2 := 84, "B", 2.5
	d := environmentDetail{Environment: "production", ProjectSlug: "backend", TotalIssues: 1,
		GeneratedAt: &now, Score: &score, Grade: &grade, Load: &load,
		Issues: []issueDetail{{IssueID: "1", Status: "unresolved", EventCount: 5}}}
	later := d
	next := now.Add(30 * time.Minute)
	later.GeneratedAt, later.Score, later.Grade, later.Load = &next, &score2, &grade2, &load2
	if environmentContentHash(d) != environmentContentHash(later) {
		t.Error("the same issues hash differently once the score decays")
	}
	changed := d
	changed.Issues = []issueDetail{{IssueID: "1", Status: "unresolved", EventCount: 6}}
	if environmentContentHash(d) == environmentContentHash(changed) {
		t.Error("a changed issue does not change the hash")
	}
}
