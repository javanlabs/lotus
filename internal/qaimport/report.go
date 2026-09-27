package qaimport

import "fmt"

type TableCount struct {
	Imported    int            `json:"imported"`
	Skipped     int            `json:"skipped"`
	SkipReasons map[string]int `json:"skip_reasons,omitempty"`
}

type Report struct {
	Tables           map[string]*TableCount `json:"tables"`
	Warnings         []string               `json:"warnings"`
	ProjectsImported int                    `json:"projects_imported"`
	ProjectsSkipped  int                    `json:"projects_skipped"`
}

func newReport() *Report { return &Report{Tables: map[string]*TableCount{}} }

func (r *Report) table(name string) *TableCount {
	t := r.Tables[name]
	if t == nil {
		t = &TableCount{SkipReasons: map[string]int{}}
		r.Tables[name] = t
	}
	return t
}

func (r *Report) imported(table string, n int) { r.table(table).Imported += n }

func (r *Report) skipped(table, reason string) {
	t := r.table(table)
	t.Skipped++
	t.SkipReasons[reason]++
}

func (r *Report) warn(format string, args ...any) {
	r.Warnings = append(r.Warnings, fmt.Sprintf(format, args...))
}

// merge adds a per-project report into r; used so a rolled-back (dry-run or
// failed) project does not leave half-counted numbers behind.
func (r *Report) merge(o *Report) {
	for name, t := range o.Tables {
		dst := r.table(name)
		dst.Imported += t.Imported
		dst.Skipped += t.Skipped
		for k, v := range t.SkipReasons {
			dst.SkipReasons[k] += v
		}
	}
	r.Warnings = append(r.Warnings, o.Warnings...)
}
