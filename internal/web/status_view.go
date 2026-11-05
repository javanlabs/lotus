package web

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/uyab/lotus/internal/collector"
	"github.com/uyab/lotus/internal/repo"
	"github.com/uyab/lotus/internal/scoring"
)

// The public status page's view, built without I/O from a project's
// recorded days (repo.ProjectStatusDays) so it is tested without a
// database. A day's value is the newest recorded day on or before it,
// carried forward without expiring (spec §6.4); a day before the first
// record is No data, never Passed.

// wib is the status page's day boundary: Jakarta, which keeps no daylight
// saving, so a fixed zone needs no tz database.
var wib = time.FixedZone("WIB", 7*3600)

// statusStripDays is how many days the strip shows. The strip is fixed:
// it always ends today, whichever day is selected, so picking a day
// highlights a bar instead of moving the strip under the reader.
const statusStripDays = 30

// statusToday is now's WIB day, at midnight.
func statusToday(now time.Time) time.Time {
	n := now.In(wib)
	return time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, wib)
}

// statusStripStart is the strip's first day: today's, statusStripDays-1
// days back.
func statusStripStart(today time.Time) time.Time {
	return today.AddDate(0, 0, -(statusStripDays - 1))
}

// parseStatusDate is the WIB day ?date= asks for, kept inside the strip:
// today when it is empty, malformed or still in the future, and the
// strip's first day when it is older than that.
func parseStatusDate(raw string, now time.Time) time.Time {
	today := statusToday(now)
	d, err := time.ParseInLocation(time.DateOnly, raw, wib)
	if err != nil || d.After(today) {
		return today
	}
	if start := statusStripStart(today); d.Before(start) {
		return start
	}
	return d
}

// statusCell is one day of the strip.
type statusCell struct {
	Day      time.Time
	Tone     string // "pass" | "fail" | "none"
	Label    string // "28 Oct 2026 · Failed", so state is never colour alone
	Href     string
	Selected bool
	// DayNum is the day of the month printed inside the bar, "1".."31".
	DayNum string
	// Month marks where a month starts in the strip: "OCT" on the first
	// bar and on every day 1 after it, empty on every other bar. Printed
	// inside the bar, above DayNum.
	Month string
}

// statusMonthMark is the strip's month marker for d: its month as an
// uppercase English abbreviation on the strip's first bar and on the first
// of a month, empty otherwise. Go's "Jan" layout is always English.
func statusMonthMark(d time.Time, first bool) string {
	if !first && d.Day() != 1 {
		return ""
	}
	return strings.ToUpper(d.Format("Jan"))
}

// statusKindCell is one integration's headline for one component: its
// score coloured by its band, and on hover what it was made of.
type statusKindCell struct{ Text, Tone, Title string }

type statusRow struct {
	Name  string
	Cells []statusKindCell
}

type statusView struct {
	OrgName, ProjectName string
	Date                 string // "2 Nov 2026"
	Score                string // empty with no data
	Verdict, Tone        string
	// Attention is the count a project's verdict always carries: "2 of
	// 18 components need attention"; empty with no data.
	Attention string
	Cells     []statusCell
	Columns   []string
	Rows      []statusRow
	UpdatedAt string // empty with no data
}

// statusDayVerdict reads a recorded status in the score bands' words.
// A day recorded since statuses are read from the score (pass at 80 or
// more) is its score's band; a day recorded before keeps the verdict it
// was given — past days are never rewritten. Anything but pass or fail —
// unknown, or no row at all — is No data.
func statusDayVerdict(status string) (verdict, tone string) {
	switch collector.Status(status) {
	case collector.StatusPass:
		return scoring.WordHealthy, "pass"
	case collector.StatusFail:
		return scoring.WordNeedsAttention, "fail"
	default:
		return scoring.ReasonNoData, "none"
	}
}

// dayOnOrBefore is the newest of days (ascending) on or before day, or nil.
func dayOnOrBefore(days []repo.ProjectStatusDay, day time.Time) *repo.ProjectStatusDay {
	key := day.Format(time.DateOnly)
	var found *repo.ProjectStatusDay
	for i := range days {
		if days[i].Day.Format(time.DateOnly) > key {
			break
		}
		found = &days[i]
	}
	return found
}

// buildStatusView is the page for selected: the strip of the 30 days ending
// today with selected's bar highlighted, and selected's verdict and
// components. days must cover the strip (statusStripStart(today)..today).
func buildStatusView(orgName string, p *repo.Project, days []repo.ProjectStatusDay, today, selected time.Time) statusView {
	v := statusView{
		OrgName: orgName, ProjectName: p.Name,
		Date: selected.Format("2 Jan 2006"),
	}

	start := statusStripStart(today)
	v.Cells = make([]statusCell, 0, statusStripDays)
	for d := start; !d.After(today); d = d.AddDate(0, 0, 1) {
		status := ""
		if row := dayOnOrBefore(days, d); row != nil {
			status = row.Status
		}
		verdict, tone := statusDayVerdict(status)
		v.Cells = append(v.Cells, statusCell{
			Day: d, Tone: tone, Label: d.Format("2 Jan 2006") + " · " + verdict,
			Href:     "/status/" + p.Slug + "?date=" + d.Format(time.DateOnly),
			Selected: d.Equal(selected),
			DayNum:   strconv.Itoa(d.Day()),
			Month:    statusMonthMark(d, d.Equal(start)),
		})
	}

	for _, k := range bindableKinds {
		v.Columns = append(v.Columns, kindLabels[k])
	}

	row := dayOnOrBefore(days, selected)
	if row == nil {
		v.Verdict, v.Tone = statusDayVerdict("")
		return v
	}
	v.Verdict, v.Tone = statusDayVerdict(row.Status)
	if v.Tone != "none" && row.Score != nil {
		v.Score = fmt.Sprintf("%d", *row.Score)
	}
	v.UpdatedAt = row.UpdatedAt.In(wib).Format("2 Jan 2006 15:04") + " WIB"
	if v.Tone != "none" && len(row.Components) > 0 {
		need := 0
		for _, comp := range row.Components {
			if repo.NeedsAttention(comp.Kinds) {
				need++
			}
		}
		v.Attention = attentionText(need, len(row.Components))
	}
	for _, comp := range row.Components {
		r := statusRow{Name: comp.Name, Cells: make([]statusKindCell, 0, len(bindableKinds))}
		for _, k := range bindableKinds {
			kh, ok := comp.Kinds[k]
			if !ok {
				r.Cells = append(r.Cells, statusKindCell{Text: "–", Tone: "none"})
				continue
			}
			r.Cells = append(r.Cells, statusKindCell{Text: homeMapCellLabel(kh), Tone: bandTone(kh.Score), Title: homeMapCellTitle(k, kh)})
		}
		v.Rows = append(v.Rows, r)
	}
	return v
}
