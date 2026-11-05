package web

import (
	"slices"
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
	for _, raw := range []string{"", "nope", "2026-13-01", "2099-01-01", "2026-11-03"} {
		if got := parseStatusDate(raw, now); !got.Equal(today) {
			t.Errorf("parseStatusDate(%q) = %v, want today %v", raw, got, today)
		}
	}
	if got := parseStatusDate("2026-10-28", now); !got.Equal(wibDay(2026, 10, 28)) {
		t.Errorf("parseStatusDate(2026-10-28) = %v", got)
	}
	if got := parseStatusDate("2026-11-02", now); !got.Equal(today) {
		t.Errorf("today itself = %v, want %v", got, today)
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

	v := buildStatusView("PT Javan Cipta Solusi", p, days, wibDay(2026, 11, 2))
	if v.Verdict != "Passed" || v.Tone != "pass" || v.Score != "84" {
		t.Fatalf("verdict = %q %q %q, want Passed pass 84 (Oct 25 carried to Nov 2)", v.Verdict, v.Tone, v.Score)
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
	if c := v.Cells[16]; c.Label != "20 Oct 2026 · Failed" || c.Href != "/status/pay?date=2026-10-20" {
		t.Errorf("Oct 20 cell = %+v", c)
	}
	if c := v.Cells[0]; c.Label != "4 Oct 2026 · No data" {
		t.Errorf("Oct 4 cell label = %q", c.Label)
	}
	if v.DateValue != "2026-11-02" || v.ProjectName != "Payments" || v.OrgName != "PT Javan Cipta Solusi" {
		t.Errorf("header = %q %q %q", v.DateValue, v.ProjectName, v.OrgName)
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
	v := buildStatusView("Acme", &repo.Project{Slug: "pay"}, []repo.ProjectStatusDay{statusDay("2026-06-01", "fail", &s)}, wibDay(2026, 11, 2))
	if v.Verdict != "Failed" || v.Score != "40" {
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
	v := buildStatusView("Acme", &repo.Project{Slug: "pay"}, nil, wibDay(2026, 11, 2))
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
	v = buildStatusView("Acme", &repo.Project{Slug: "pay"}, []repo.ProjectStatusDay{statusDay("2026-11-02", "unknown", &s)}, wibDay(2026, 11, 2))
	if v.Verdict != "No data" || v.Tone != "none" || v.Score != "" || v.Cells[29].Tone != "none" {
		t.Fatalf("unknown day: verdict %q tone %q score %q cell %q", v.Verdict, v.Tone, v.Score, v.Cells[29].Tone)
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
	v := buildStatusView("Acme", &repo.Project{Slug: "pay"}, []repo.ProjectStatusDay{day}, wibDay(2026, 11, 2))
	if !slices.Equal(v.Columns, want) {
		t.Fatalf("columns = %v, want %v", v.Columns, want)
	}
	if len(v.Rows) != 1 || v.Rows[0].Name != "API" || len(v.Rows[0].Cells) != len(bindableKinds) {
		t.Fatalf("rows = %+v", v.Rows)
	}
	gitlab := slices.Index(bindableKinds, "gitlab")
	for i, c := range v.Rows[0].Cells {
		if i == gitlab {
			if c.Text != "77" || c.Tone != "pass" {
				t.Errorf("gitlab cell = %+v, want 77 pass", c)
			}
			continue
		}
		if c.Text != "–" || c.Tone != "none" {
			t.Errorf("cell %d (%s) = %+v, want – none", i, bindableKinds[i], c)
		}
	}
}
