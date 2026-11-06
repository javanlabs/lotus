package web

import (
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/uyab/lotus/internal/repo"
)

func statusDay(day string, status string, score *int, components ...repo.StatusDayComponent) repo.ProjectStatusDay {
	d, err := time.Parse(time.DateOnly, day)
	if err != nil {
		panic(err)
	}
	if components == nil {
		components = []repo.StatusDayComponent{}
	}
	return repo.ProjectStatusDay{Day: d, Status: status, Score: score, Components: components,
		UpdatedAt: d.Add(10 * time.Hour)}
}

func wibDay(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, wib) }

func TestParseStatusDate(t *testing.T) {
	t.Parallel()
	// 23:30 UTC on 1 Nov is already 2 Nov in Jakarta.
	now := time.Date(2026, 11, 1, 23, 30, 0, 0, time.UTC)
	today := wibDay(2026, 11, 2)
	if got := statusToday(now); !got.Equal(today) {
		t.Fatalf("statusToday = %v, want %v", got, today)
	}
	// Empty, invalid or future: today.
	for _, raw := range []string{"", "nope", "2026-13-01", "2099-01-01", "2026-11-03"} {
		if got := parseStatusDate(raw, now); !got.Equal(today) {
			t.Errorf("parseStatusDate(%q) = %v, want today %v", raw, got, today)
		}
	}
	// Inside the strip (Oct 4 – Nov 2), including both ends: as asked.
	for _, want := range []time.Time{wibDay(2026, 10, 4), wibDay(2026, 10, 28), today} {
		if got := parseStatusDate(want.Format(time.DateOnly), now); !got.Equal(want) {
			t.Errorf("parseStatusDate(%s) = %v, want %v", want.Format(time.DateOnly), got, want)
		}
	}
	// Older than the strip: its first day.
	for _, raw := range []string{"2026-10-03", "2026-09-01", "2020-01-01"} {
		if got := parseStatusDate(raw, now); !got.Equal(wibDay(2026, 10, 4)) {
			t.Errorf("parseStatusDate(%q) = %v, want the strip's first day 2026-10-04", raw, got)
		}
	}
}

// The strip ends today whichever day is selected; the selected day only
// moves the highlight, the verdict and the components.
func TestBuildStatusView_StripIsFixedToToday(t *testing.T) {
	t.Parallel()
	p := &repo.Project{Slug: "pay"}
	s62, s84 := 62, 84
	days := []repo.ProjectStatusDay{
		statusDay("2026-10-20", "fail", &s62, repo.StatusDayComponent{Name: "ledger-api"}),
		statusDay("2026-10-25", "pass", &s84),
	}
	today, selected := wibDay(2026, 11, 2), wibDay(2026, 10, 22)

	v := buildStatusView("Acme", p, days, today, selected)
	if len(v.Cells) != statusStripDays {
		t.Fatalf("%d cells, want %d", len(v.Cells), statusStripDays)
	}
	if first, last := v.Cells[0].Day, v.Cells[len(v.Cells)-1].Day; !first.Equal(wibDay(2026, 10, 4)) || !last.Equal(today) {
		t.Fatalf("strip = %s..%s, want 2026-10-04..2026-11-02", first.Format(time.DateOnly), last.Format(time.DateOnly))
	}
	var sel []int
	for i, c := range v.Cells {
		if c.Selected {
			sel = append(sel, i)
		}
	}
	if !slices.Equal(sel, []int{18}) || !v.Cells[18].Day.Equal(selected) {
		t.Fatalf("selected cells = %v, want only 18 (Oct 22)", sel)
	}
	// The verdict is the selected day's (Oct 20 carried to Oct 22), not
	// today's (Oct 25 carried to Nov 2).
	if v.Date != "22 Oct 2026" || v.Verdict != "Fail" || v.Score != "62" {
		t.Errorf("verdict = %q %q %q, want 22 Oct 2026 Fail 62", v.Date, v.Verdict, v.Score)
	}
	if len(v.Rows) != 1 || v.Rows[0].Name != "ledger-api" {
		t.Errorf("rows = %+v, want Oct 20's ledger-api", v.Rows)
	}
	// The bars still read their own days: after Oct 25 they pass.
	if got := v.Cells[29].Tone; got != "pass" {
		t.Errorf("today's bar = %q, want pass", got)
	}

	// Another selected day: the very same strip, the highlight moved.
	w := buildStatusView("Acme", p, days, today, wibDay(2026, 10, 4))
	for i := range v.Cells {
		if !w.Cells[i].Day.Equal(v.Cells[i].Day) || w.Cells[i].Tone != v.Cells[i].Tone || w.Cells[i].Month != v.Cells[i].Month {
			t.Fatalf("cell %d differs between selections: %+v vs %+v", i, w.Cells[i], v.Cells[i])
		}
		if w.Cells[i].Selected != (i == 0) {
			t.Errorf("Oct 4 selected: cell %d Selected = %v", i, w.Cells[i].Selected)
		}
	}
}

func cellTones(cells []statusCell) map[string]string {
	out := map[string]string{}
	for _, c := range cells {
		out[c.Day.Format(time.DateOnly)] = c.Tone
	}
	return out
}

func TestBuildStatusView_CarriesTheLastDayForward(t *testing.T) {
	t.Parallel()
	p := &repo.Project{Slug: "pay", Name: "Payments"}
	s62, s84 := 62, 84
	days := []repo.ProjectStatusDay{statusDay("2026-10-20", "fail", &s62), statusDay("2026-10-25", "pass", &s84)}

	v := buildStatusView("PT Javan Cipta Solusi", p, days, wibDay(2026, 11, 2), wibDay(2026, 11, 2))
	if v.Verdict != "Pass" || v.Tone != "pass" || v.Score != "84" {
		t.Fatalf("verdict = %q %q %q, want Pass pass 84 (Oct 25 carried to Nov 2)", v.Verdict, v.Tone, v.Score)
	}
	if len(v.Cells) != 30 {
		t.Fatalf("%d cells, want 30", len(v.Cells))
	}
	if first, last := v.Cells[0], v.Cells[29]; first.Day.Format(time.DateOnly) != "2026-10-04" || last.Day.Format(time.DateOnly) != "2026-11-02" {
		t.Fatalf("strip = %s..%s, want 2026-10-04..2026-11-02", first.Day.Format(time.DateOnly), last.Day.Format(time.DateOnly))
	}
	tones := cellTones(v.Cells)
	for d := wibDay(2026, 10, 4); !d.After(wibDay(2026, 11, 2)); d = d.AddDate(0, 0, 1) {
		want := "pass"
		switch {
		case d.Before(wibDay(2026, 10, 20)):
			want = "none"
		case d.Before(wibDay(2026, 10, 25)):
			want = "fail"
		}
		if got := tones[d.Format(time.DateOnly)]; got != want {
			t.Errorf("cell %s = %q, want %q", d.Format(time.DateOnly), got, want)
		}
	}
	for i, c := range v.Cells {
		if c.Selected != (i == 29) {
			t.Errorf("cell %d Selected = %v", i, c.Selected)
		}
	}
	if c := v.Cells[16]; c.Label != "20 Oct 2026 · Fail" || c.Href != "/status/pay?date=2026-10-20" {
		t.Errorf("Oct 20 cell = %+v", c)
	}
	if c := v.Cells[0]; c.Label != "4 Oct 2026 · No data" {
		t.Errorf("Oct 4 cell label = %q", c.Label)
	}
	if v.Date != "2 Nov 2026" || v.ProjectName != "Payments" || v.OrgName != "PT Javan Cipta Solusi" {
		t.Errorf("header = %q %q %q", v.Date, v.ProjectName, v.OrgName)
	}
	if v.UpdatedAt == "" {
		t.Errorf("no updated time from the carried row")
	}
}

// A sync that stopped never expires: the last row is carried to any later
// date (review focus 1).
func TestBuildStatusView_CarriesPastTheLastRecordedDay(t *testing.T) {
	t.Parallel()
	s := 40
	v := buildStatusView("Acme", &repo.Project{Slug: "pay"}, []repo.ProjectStatusDay{statusDay("2026-06-01", "fail", &s)}, wibDay(2026, 11, 2), wibDay(2026, 11, 2))
	if v.Verdict != "Fail" || v.Score != "40" {
		t.Fatalf("verdict = %q %q, want the June row carried forward", v.Verdict, v.Score)
	}
	for _, c := range v.Cells {
		if c.Tone != "fail" {
			t.Fatalf("cell %s = %q, want fail", c.Day.Format(time.DateOnly), c.Tone)
		}
	}
}

func TestBuildStatusView_NoDataIsNeverPassed(t *testing.T) {
	t.Parallel()
	v := buildStatusView("Acme", &repo.Project{Slug: "pay"}, nil, wibDay(2026, 11, 2), wibDay(2026, 11, 2))
	if v.Verdict != "No data" || v.Tone != "none" || v.Score != "" {
		t.Fatalf("no rows: verdict %q tone %q score %q, want No data, none, empty", v.Verdict, v.Tone, v.Score)
	}
	for _, c := range v.Cells {
		if c.Tone != "none" {
			t.Errorf("cell %s = %q, want none", c.Day.Format(time.DateOnly), c.Tone)
		}
	}
	if len(v.Rows) != 0 {
		t.Errorf("rows = %+v, want none", v.Rows)
	}

	// A recorded unknown day (nothing measured) is No data too, even with
	// a score left on it.
	s := 90
	v = buildStatusView("Acme", &repo.Project{Slug: "pay"}, []repo.ProjectStatusDay{statusDay("2026-11-02", "unknown", &s)}, wibDay(2026, 11, 2), wibDay(2026, 11, 2))
	if v.Verdict != "No data" || v.Tone != "none" || v.Score != "" || v.Cells[29].Tone != "none" {
		t.Fatalf("unknown day: verdict %q tone %q score %q cell %q", v.Verdict, v.Tone, v.Score, v.Cells[29].Tone)
	}
}

// A day's colour comes from its score. A row recorded before statuses
// were read from the score may carry a status that disagrees with it;
// the page must show the number's band, never "83" in red.
func TestBuildStatusView_VerdictIsTheScoresBand(t *testing.T) {
	t.Parallel()
	s83, s64 := 83, 64
	days := []repo.ProjectStatusDay{
		statusDay("2026-10-30", "fail", &s83),
		statusDay("2026-11-02", "pass", &s64),
	}
	p := &repo.Project{Slug: "pay"}

	v := buildStatusView("Acme", p, days, wibDay(2026, 11, 2), wibDay(2026, 10, 31))
	if v.Score != "83" || v.Verdict != "Pass" || v.Tone != "pass" {
		t.Errorf("83 stored as fail: %q %q %q, want 83 Pass pass", v.Score, v.Verdict, v.Tone)
	}
	v = buildStatusView("Acme", p, days, wibDay(2026, 11, 2), wibDay(2026, 11, 2))
	if v.Score != "64" || v.Verdict != "Fail" || v.Tone != "fail" {
		t.Errorf("64 stored as pass: %q %q %q, want 64 Fail fail", v.Score, v.Verdict, v.Tone)
	}
	tones := cellTones(v.Cells)
	if tones["2026-10-31"] != "pass" || tones["2026-11-02"] != "fail" {
		t.Errorf("bars = %v, want Oct 31 pass and Nov 2 fail", tones)
	}
}

// stripLabels is the strip as it reads under the bars: "OCT 7" where a
// month is marked, "8" where only the day is.
func stripLabels(cells []statusCell) []string {
	out := make([]string, len(cells))
	for i, c := range cells {
		out[i] = c.DayNum
		if c.Month != "" {
			out[i] = c.Month + " " + c.DayNum
		}
	}
	return out
}

// marked is the labels that carry a month marker.
func marked(labels []string) []string {
	var out []string
	for _, l := range labels {
		if strings.Contains(l, " ") {
			out = append(out, l)
		}
	}
	return out
}

func TestBuildStatusView_StripLabels(t *testing.T) {
	t.Parallel()
	p := &repo.Project{Slug: "pay"}
	for _, tc := range []struct {
		name        string
		today       time.Time
		first, last string
		wantMarked  []string
		markedAt    []int
	}{
		{
			name: "crosses a month", today: wibDay(2026, 11, 5),
			first: "OCT 7", last: "5", wantMarked: []string{"OCT 7", "NOV 1"}, markedAt: []int{0, 25},
		},
		{
			name: "crosses a year", today: wibDay(2027, 1, 10),
			first: "DEC 12", last: "10", wantMarked: []string{"DEC 12", "JAN 1"}, markedAt: []int{0, 20},
		},
		{
			// 30 days inside one month: only the first bar is marked.
			name: "one month", today: wibDay(2026, 10, 31),
			first: "OCT 2", last: "31", wantMarked: []string{"OCT 2"}, markedAt: []int{0},
		},
		{
			// The strip starts on the 1st: one marker, not two.
			name: "starts on the first", today: wibDay(2026, 10, 30),
			first: "OCT 1", last: "30", wantMarked: []string{"OCT 1"}, markedAt: []int{0},
		},
		{
			// February is shorter than the strip, so the strip touches
			// three months.
			name: "spans three months", today: wibDay(2027, 3, 1),
			first: "JAN 31", last: "MAR 1", wantMarked: []string{"JAN 31", "FEB 1", "MAR 1"}, markedAt: []int{0, 1, 29},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			v := buildStatusView("Acme", p, nil, tc.today, tc.today)
			labels := stripLabels(v.Cells)
			if len(labels) != statusStripDays {
				t.Fatalf("%d labels, want %d", len(labels), statusStripDays)
			}
			if labels[0] != tc.first || labels[len(labels)-1] != tc.last {
				t.Errorf("strip reads %q..%q, want %q..%q", labels[0], labels[len(labels)-1], tc.first, tc.last)
			}
			if got := marked(labels); !slices.Equal(got, tc.wantMarked) {
				t.Errorf("marked = %v, want %v (strip %v)", got, tc.wantMarked, labels)
			}
			for _, i := range tc.markedAt {
				if v.Cells[i].Month == "" {
					t.Errorf("cell %d (%s) has no month marker", i, v.Cells[i].Day.Format(time.DateOnly))
				}
			}
			for i, c := range v.Cells {
				if want := strconv.Itoa(c.Day.Day()); c.DayNum != want {
					t.Errorf("cell %d DayNum = %q, want %q", i, c.DayNum, want)
				}
			}
		})
	}
}

func TestBuildStatusView_ColumnsFollowBindableKinds(t *testing.T) {
	t.Parallel()
	want := make([]string, len(bindableKinds))
	for i, k := range bindableKinds {
		want[i] = kindLabels[k]
	}
	gitlabScore := 77
	day := statusDay("2026-11-02", "pass", &gitlabScore, repo.StatusDayComponent{
		Name:  "API",
		Kinds: map[string]repo.KindHeadline{"gitlab": {Status: "pass", Score: &gitlabScore}},
	})
	v := buildStatusView("Acme", &repo.Project{Slug: "pay"}, []repo.ProjectStatusDay{day}, wibDay(2026, 11, 2), wibDay(2026, 11, 2))
	if !slices.Equal(v.Columns, want) {
		t.Fatalf("columns = %v, want %v", v.Columns, want)
	}
	if len(v.Rows) != 1 || v.Rows[0].Name != "API" || len(v.Rows[0].Cells) != len(bindableKinds) {
		t.Fatalf("rows = %+v", v.Rows)
	}
	gitlab := slices.Index(bindableKinds, "gitlab")
	for i, c := range v.Rows[0].Cells {
		if i == gitlab {
			// The cell is coloured by its score's band, not the status
			// stored beside it: 77 needs attention.
			if c.Text != "77" || c.Tone != "fail" || c.Title != "GitLab: 77 · Fail" {
				t.Errorf("gitlab cell = %+v, want 77 fail", c)
			}
			continue
		}
		if c.Text != "–" || c.Tone != "none" {
			t.Errorf("cell %d (%s) = %+v, want – none", i, bindableKinds[i], c)
		}
	}
}

// The verdict carries the count of components needing attention — any
// kind below 80 — however healthy the project's own score reads.
func TestBuildStatusView_VerdictCountsComponentsNeedingAttention(t *testing.T) {
	t.Parallel()
	s96, s70, s19, s88 := 96, 70, 19, 88
	day := statusDay("2026-11-02", "pass", &s88,
		repo.StatusDayComponent{Name: "API", Kinds: map[string]repo.KindHeadline{"sonarqube": {Status: "pass", Score: &s96}, "sentry": {Status: "fail", Score: &s70}}},
		repo.StatusDayComponent{Name: "Web", Kinds: map[string]repo.KindHeadline{"sonarqube": {Status: "pass", Score: &s96}, "gitlab": {Status: "unknown"}}},
	)
	v := buildStatusView("Acme", &repo.Project{Slug: "pay"}, []repo.ProjectStatusDay{day}, wibDay(2026, 11, 2), wibDay(2026, 11, 2))
	// Rolled up from the components, as the project page does: sonarqube
	// averages 96, sentry 70, so 83 — whatever score was stored (88).
	if v.Score != "83" || v.Verdict != "Pass" || v.Attention != "1 of 2 components fail" {
		t.Fatalf("verdict = %q %q %q", v.Score, v.Verdict, v.Attention)
	}

	// A component that fails (API averages 58) caps the project at 58, as
	// on the project page, though 88 was stored.
	day = statusDay("2026-11-02", "pass", &s88,
		repo.StatusDayComponent{Name: "API", Kinds: map[string]repo.KindHeadline{"sonarqube": {Status: "pass", Score: &s96}, "sentry": {Status: "fail", Score: &s19}}},
		repo.StatusDayComponent{Name: "Web", Kinds: map[string]repo.KindHeadline{"sonarqube": {Status: "pass", Score: &s96}}},
	)
	v = buildStatusView("Acme", &repo.Project{Slug: "pay"}, []repo.ProjectStatusDay{day}, wibDay(2026, 11, 2), wibDay(2026, 11, 2))
	if v.Score != "58" || v.Verdict != "Fail" || v.Tone != "fail" || v.Cells[29].Tone != "fail" {
		t.Fatalf("capped: %q %q %q bar %q, want 58 Fail fail", v.Score, v.Verdict, v.Tone, v.Cells[29].Tone)
	}
	if empty := buildStatusView("Acme", &repo.Project{Slug: "pay"}, nil, wibDay(2026, 11, 2), wibDay(2026, 11, 2)); empty.Attention != "" {
		t.Fatalf("no data still counts components: %q", empty.Attention)
	}
}
