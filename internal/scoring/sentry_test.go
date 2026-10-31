package scoring

import (
	"database/sql"
	"math"
	"testing"
	"time"
)

var scoreNow = time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)

// sig builds one issue signal, daysAgo before the fixed "now".
func sig(substatus string, events int, daysAgo float64) SentryIssueSignal {
	return SentryIssueSignal{
		Substatus:  substatus,
		EventCount: events,
		LastSeen:   sql.NullTime{Time: scoreNow.Add(-time.Duration(daysAgo * float64(24*time.Hour))), Valid: true},
	}
}

func TestSentryLoadWeightsSubstatusByTriageCost(t *testing.T) {
	// Same volume, same recency: only the substatus differs, so the ordering
	// escalating > regressed > new > ongoing must hold.
	const events, days = 100, 0
	var last float64 = math.Inf(1)
	for _, s := range []string{"escalating", "regressed", "new", "ongoing"} {
		load := sentryLoad([]SentryIssueSignal{sig(s, events, days)}, scoreNow)
		if load >= last {
			t.Errorf("%s load = %.3f, expected less than the previous state's %.3f", s, load, last)
		}
		last = load
	}
	// An unrecognised substatus must still count, not vanish.
	if got := sentryLoad([]SentryIssueSignal{sig("archived_until_escalating", events, days)}, scoreNow); got <= 0 {
		t.Errorf("unknown substatus load = %.3f, want > 0", got)
	}
}

func TestSentryLoadCompressesVolumeLogarithmically(t *testing.T) {
	// 100x the events must cost far less than 100x the load — that is the whole
	// point of the log term: one chatty issue must not bury everything else.
	quiet := sentryLoad([]SentryIssueSignal{sig("ongoing", 10, 0)}, scoreNow)
	loud := sentryLoad([]SentryIssueSignal{sig("ongoing", 1000, 0)}, scoreNow)
	if loud <= quiet {
		t.Fatalf("loud (%.3f) should exceed quiet (%.3f)", loud, quiet)
	}
	if ratio := loud / quiet; ratio > 4 {
		t.Errorf("100x events multiplied load by %.1fx; log compression should keep it small", ratio)
	}
}

func TestSentryLoadDecaysWithRecency(t *testing.T) {
	fresh := sentryLoad([]SentryIssueSignal{sig("ongoing", 100, 0)}, scoreNow)
	weekOld := sentryLoad([]SentryIssueSignal{sig("ongoing", 100, 7)}, scoreNow)
	if diff := math.Abs(weekOld - fresh/2); diff > 0.01 {
		t.Errorf("after one half-life load = %.3f, want ~%.3f (half of %.3f)", weekOld, fresh/2, fresh)
	}
	// Missing last_seen must not silently zero an issue out.
	noTime := sentryLoad([]SentryIssueSignal{{Substatus: "ongoing", EventCount: 100}}, scoreNow)
	if math.Abs(noTime-fresh) > 0.01 {
		t.Errorf("issue without last_seen load = %.3f, want it treated as current (%.3f)", noTime, fresh)
	}
}

func TestSentryLoadCountsIssuesWithoutEvents(t *testing.T) {
	if got := sentryLoad([]SentryIssueSignal{sig("new", 0, 0)}, scoreNow); got <= 0 {
		t.Errorf("issue with 0 events load = %.3f, want > 0 (count is floored at 1)", got)
	}
}

func TestSentryScoreIsMonotonicAndBounded(t *testing.T) {
	prev := 101.0
	for load := 0.0; load <= 200; load += 5 {
		s := sentryScoreFromLoad(load)
		if s > 100 || s < 0 {
			t.Fatalf("load %.0f → score %.2f, outside 0..100", load, s)
		}
		if s >= prev {
			t.Fatalf("load %.0f → score %.2f did not decrease from %.2f", load, s, prev)
		}
		prev = s
	}
}

func TestSentryGradeBands(t *testing.T) {
	// Conventional percentage scale: 90 / 80 / 70 / 60.
	cases := []struct {
		score float64
		want  string
	}{
		{100, "A"}, {90, "A"}, {89.9, "B"}, {80, "B"},
		{79.9, "C"}, {70, "C"}, {69.9, "D"}, {60, "D"}, {59.9, "E"}, {0, "E"},
	}
	for _, c := range cases {
		if got := sentryGrade(c.score); got != c.want {
			t.Errorf("grade(%.1f) = %s, want %s", c.score, got, c.want)
		}
	}
}

func TestEvalSentryStatus(t *testing.T) {
	cases := []struct {
		name       string
		mapped     bool
		hasSnap    bool
		sawEvents  bool
		signals    []SentryIssueSignal
		wantKind   SentryStatusKind
		wantFailed bool
	}{
		{"no sentry binding", false, false, false, nil, SentryStatusNotMapped, true},
		{"mapped but never pulled", true, false, false, nil, SentryStatusNoData, true},
		{"zero issues, never reported", true, true, false, nil, SentryStatusNoSignal, false},
		{"zero issues, has reported", true, true, true, nil, SentryStatusClean, false},
		{"reporting issues", true, true, true, []SentryIssueSignal{sig("ongoing", 5, 0)}, SentryStatusGraded, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := evalSentryStatus(c.mapped, c.hasSnap, c.sawEvents, c.signals, scoreNow)
			if got.Kind != c.wantKind {
				t.Errorf("kind = %s, want %s", got.Kind, c.wantKind)
			}
			if got.Failed != c.wantFailed {
				t.Errorf("failed = %v, want %v", got.Failed, c.wantFailed)
			}
			if c.wantFailed && got.Detail == "" {
				t.Error("a failing verdict must explain itself")
			}
		})
	}
}

// Zero issues means two different things, and only one of them is a score.
func TestEvalSentryStatusSeparatesCleanFromSilent(t *testing.T) {
	clean := evalSentryStatus(true, true, true, nil, scoreNow)
	if clean.Score != 100 || clean.Grade != "A" {
		t.Errorf("clean = score %d grade %q, want 100 A", clean.Score, clean.Grade)
	}
	silent := evalSentryStatus(true, true, false, nil, scoreNow)
	if silent.Grade != "" || silent.Label != "no signal" {
		t.Errorf("silent = grade %q label %q, want no grade", silent.Grade, silent.Label)
	}
	if clean.Detail == silent.Detail {
		t.Error("the two zero-issue states must explain themselves differently")
	}
}

func TestEvalSentryStatusGradesRealisticRepos(t *testing.T) {
	// Anchors the calibration: these are the shapes the bands were tuned for,
	// so a change to the weights or scale that moves them shows up here.
	cases := []struct {
		name      string
		signals   []SentryIssueSignal
		wantGrade string
	}{
		{
			name:      "one stale low-volume issue",
			signals:   []SentryIssueSignal{sig("ongoing", 5, 3)},
			wantGrade: "A",
		},
		{
			name:      "two quiet known issues",
			signals:   []SentryIssueSignal{sig("ongoing", 50, 1), sig("ongoing", 30, 2)},
			wantGrade: "B",
		},
		{
			name: "four fresh known issues",
			signals: []SentryIssueSignal{
				sig("ongoing", 100, 0), sig("ongoing", 100, 0),
				sig("ongoing", 100, 0), sig("ongoing", 100, 0),
			},
			wantGrade: "C",
		},
		{
			name: "a regression on top of a small backlog",
			signals: []SentryIssueSignal{
				sig("regressed", 45, 0),
				sig("ongoing", 100, 0), sig("ongoing", 100, 0), sig("ongoing", 100, 0),
			},
			wantGrade: "D",
		},
		{
			name:      "one escalating issue with 3.4k events",
			signals:   []SentryIssueSignal{sig("escalating", 3414, 0)},
			wantGrade: "E",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := evalSentryStatus(true, true, true, c.signals, scoreNow)
			if got.Grade != c.wantGrade {
				t.Errorf("grade = %s (score %d, load %.2f), want %s", got.Grade, got.Score, got.Load, c.wantGrade)
			}
		})
	}
}

func TestSentryGradeLabelCoversEveryBand(t *testing.T) {
	// The dashboard shows only the word, so every band must have one — a missing
	// label would render an empty badge rather than an obviously wrong one.
	want := map[string]string{"A": "great", "B": "good", "C": "risk", "D": "poor", "E": "bad"}
	for grade, label := range want {
		if got := sentryGradeLabel(grade); got != label {
			t.Errorf("sentryGradeLabel(%q) = %q, want %q", grade, got, label)
		}
	}
	// Every grade sentryGrade can produce must map to a label.
	for _, score := range []float64{100, 90, 85, 80, 75, 70, 65, 60, 50, 0} {
		if sentryGradeLabel(sentryGrade(score)) == "" {
			t.Errorf("score %.0f → grade %q has no label", score, sentryGrade(score))
		}
	}
	if got := sentryGradeLabel(""); got != "" {
		t.Errorf("empty grade should have no label, got %q", got)
	}
}
