package web

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/repo"
)

// The four kinds' columns and summary cards, ported from the legacy fleet
// pages (handlers/{sonar,trivy,sentry,gitlab}.go and their templates) and
// scoped to one project's bindings. Each builder fills the rows
// newKindPage started, in the same order as the columns it declares.

// countStat is a summary card over a count where any is bad: fail when
// it is above zero, pass at zero, none when nothing reported it.
func countStat(label string, n int64, reported bool, failOnAny bool) kindStat {
	st := kindStat{Label: label, Value: kindMissing, Tone: "none"}
	if !reported {
		return st
	}
	st.Value = thousands(n)
	if failOnAny {
		st.Tone = "pass"
		if n > 0 {
			st.Tone = "fail"
		}
	}
	return st
}

// missingCells is a row whose binding has no snapshot yet: "–" in every
// column, a blank pill in the status column.
func missingCells(cols []kindColumn, statusCol int) []kindCell {
	cells := make([]kindCell, len(cols))
	for i := range cells {
		cells[i] = kindCell{Text: kindMissing}
	}
	if statusCol >= 0 {
		cells[statusCol] = kindCell{Type: "badge", Badge: badgeCell{Text: kindMissing, Tone: "none"}}
	}
	return cells
}

// --- SonarQube -----------------------------------------------------------

const sonarGateCol = 10

func buildSonarKindPage(b *kindPageBase, snaps []repo.SonarSnapshot) kindPage {
	b.setColumns([]kindColumn{
		{Label: "LAST SCAN", Width: "minmax(70px,.7fr)", Sort: "num", Title: "When SonarQube last analysed the code"},
		{Label: "COVERAGE", Width: "minmax(62px,.6fr)", Sort: "num", Right: true, Title: "Line coverage; under 90% is drawn red"},
		{Label: "TESTS", Width: "minmax(50px,.5fr)", Sort: "num", Right: true, Title: "Unit-test execution time"},
		{Label: "HOT", Width: "minmax(40px,.45fr)", Sort: "num", Right: true, Title: "Security hotspots to review"},
		{Label: "BUGS", Width: "minmax(40px,.45fr)", Sort: "num", Right: true, Title: "Any bug is drawn red"},
		{Label: "SMELLS", Width: "minmax(48px,.45fr)", Sort: "num", Right: true, Title: "Code smells; more than 100 is drawn red"},
		{Label: "DUP", Width: "minmax(46px,.45fr)", Sort: "num", Right: true, Title: "Duplicated lines; over 30% is drawn red"},
		{Label: "VULN", Width: "minmax(40px,.45fr)", Sort: "num", Right: true, Title: "Any vulnerability is drawn red"},
		{Label: "LOC", Width: "minmax(50px,.5fr)", Sort: "num", Right: true, Title: "Lines of code"},
		{Label: "R · S · M", Width: "76px", Title: "Reliability · Security · Maintainability ratings, A best to E worst"},
		{Label: "GATE", Width: "minmax(60px,.55fr)", Sort: "num", Title: "Quality gate"},
	})

	bySnap := make(map[uuid.UUID]repo.SonarSnapshot, len(snaps))
	for _, s := range snaps {
		bySnap[s.BindingID] = s
	}
	var covSum float64
	var covN, reported int
	var loc, bugs, vulns, hotspots, smells int64
	syncedAt := make([]*time.Time, len(b.page.Rows))
	for i := range b.page.Rows {
		row, bd := &b.page.Rows[i], b.bindings[i]
		if base := strings.TrimRight(bd.ConnectionURL, "/"); base != "" {
			row.ProviderURL = base + "/dashboard?id=" + url.QueryEscape(bd.ExternalKey)
		}
		snap, ok := bySnap[bd.BindingID]
		if !ok {
			row.Cells = missingCells(b.page.Columns, sonarGateCol)
			continue
		}
		reported++
		syncedAt[i] = &snap.CollectedAt
		v := buildSonarDetailView(&snap)

		lastScan := timeCell(snap.LastScanAt)
		coverage := kindCell{Text: v.Coverage, Fail: v.CoverageFailed}
		if snap.Coverage != nil {
			coverage.Sort = sortFloat(*snap.Coverage)
			covSum += *snap.Coverage
			covN++
		}
		tests := kindCell{Text: v.TestDuration}
		if snap.TestExecutionTimeMs != nil {
			tests.Sort = sortInt(*snap.TestExecutionTimeMs)
		}
		dup := kindCell{Text: v.Duplications, Fail: v.DuplicationsFailed}
		if snap.DuplicatedLinesDensity != nil {
			dup.Sort = sortFloat(-*snap.DuplicatedLinesDensity)
		}
		gate := badgeCell{Text: kindMissing, Tone: "none"}
		gateSort := ""
		switch v.QualityGate {
		case "OK":
			gate, gateSort = badgeCell{Text: "passed", Tone: "pass"}, "1"
		case "ERROR", "WARN":
			gate, gateSort = badgeCell{Text: "failed", Tone: "fail"}, "0"
		}
		row.Cells = []kindCell{
			lastScan,
			coverage,
			tests,
			countCell(snap.SecurityHotspots, false),
			countCell(snap.Bugs, v.BugsFailed),
			countCell(snap.CodeSmells, v.CodeSmellsFailed),
			dup,
			countCell(snap.Vulnerabilities, v.VulnerabilitiesFailed),
			countCell(snap.NCLOC, false),
			{Type: "grades", Grades: []badgeCell{
				sonarGrade(v.Reliability, "Reliability"), sonarGrade(v.Security, "Security"), sonarGrade(v.Maintainability, "Maintainability"),
			}},
			{Type: "badge", Badge: gate, Sort: gateSort},
		}
		for _, p := range []struct {
			sum *int64
			v   *int
		}{{&loc, snap.NCLOC}, {&bugs, snap.Bugs}, {&vulns, snap.Vulnerabilities}, {&hotspots, snap.SecurityHotspots}, {&smells, snap.CodeSmells}} {
			if p.v != nil {
				*p.sum += int64(*p.v)
			}
		}
	}

	avg := kindStat{Label: "AVG COVERAGE", Value: kindMissing, Tone: "none"}
	if covN > 0 {
		a := covSum / float64(covN)
		avg.Value = fmt.Sprintf("%.1f%%", a)
		avg.Sub = fmt.Sprintf("%d component%s", covN, plural(covN, "", "s"))
		avg.Tone = "pass"
		if a < 90 {
			avg.Tone = "fail"
		}
	}
	has := reported > 0
	b.page.Stats = []kindStat{
		avg,
		countStat("LINES OF CODE", loc, has, false),
		countStat("BUGS", bugs, has, true),
		countStat("VULNERABILITIES", vulns, has, true),
		countStat("SECURITY HOTSPOTS", hotspots, has, false),
		countStat("CODE SMELLS", smells, has, false),
	}
	return b.finish(sonarGateCol, func(i int) *time.Time { return syncedAt[i] })
}

// sonarGrade is one SonarQube rating letter as a pill: A passes, C to E
// fail, B is neither.
func sonarGrade(letter, what string) badgeCell {
	tone := "none"
	switch letter {
	case "A":
		tone = "pass"
	case "C", "D", "E":
		tone = "fail"
	}
	return badgeCell{Text: letter, Tone: tone, Title: what + " rating " + letter}
}

// --- Trivy ---------------------------------------------------------------

const trivySeverityCol = 1

func buildTrivyKindPage(b *kindPageBase, snaps []repo.TrivySnapshot, falsePositives map[uuid.UUID]int) kindPage {
	b.setColumns([]kindColumn{
		{Label: "TAGS", Width: "minmax(100px,1fr)", Sort: "text", Title: "The scanned artifact's tags"},
		{Label: "SEVERITY", Width: "minmax(70px,.6fr)", Sort: "num", Title: "The worst finding in the image"},
		{Label: "CRIT", Width: "minmax(40px,.4fr)", Sort: "num", Right: true, Title: "Critical findings"},
		{Label: "HIGH", Width: "minmax(40px,.4fr)", Sort: "num", Right: true, Title: "High findings"},
		{Label: "MED", Width: "minmax(40px,.4fr)", Sort: "num", Right: true, Title: "Medium findings"},
		{Label: "LOW", Width: "minmax(40px,.4fr)", Sort: "num", Right: true, Title: "Low findings"},
		{Label: "FIXABLE", Width: "minmax(54px,.45fr)", Sort: "num", Right: true, Title: "Findings with a fixed version available"},
		{Label: "FALSE POS.", Width: "minmax(66px,.5fr)", Sort: "num", Right: true, Title: "Findings annotated as false positives"},
		{Label: "SCANNED", Width: "minmax(72px,.7fr)", Sort: "num", Title: "When Trivy last scanned the image"},
		{Label: "PULLS", Width: "minmax(48px,.45fr)", Sort: "num", Right: true, Title: "Registry pulls"},
	})
	bySnap := make(map[uuid.UUID]repo.TrivySnapshot, len(snaps))
	for _, s := range snaps {
		bySnap[s.BindingID] = s
	}
	var crit, high, med, low, fixable int64
	reported := 0
	syncedAt := make([]*time.Time, len(b.page.Rows))
	for i := range b.page.Rows {
		row, bd := &b.page.Rows[i], b.bindings[i]
		row.ProviderURL = harborRepoURL(bd.ConnectionURL, bd.ExternalKey)
		if bd.RepoScan() {
			// A repository scan reads a GitLab project, not a Harbor image.
			row.ProviderURL = strings.TrimRight(bd.ConnectionURL, "/") + "/" + strings.Trim(bd.ExternalKey, "/")
		}
		snap, ok := bySnap[bd.BindingID]
		if !ok {
			row.Cells = missingCells(b.page.Columns, trivySeverityCol)
			continue
		}
		reported++
		syncedAt[i] = &snap.CollectedAt
		crit, high, med, low, fixable = crit+int64(snap.Critical), high+int64(snap.High), med+int64(snap.Medium), low+int64(snap.Low), fixable+int64(snap.Fixable)

		tags := strings.Join([]string(snap.Tags), ", ")
		tagCell := kindCell{Text: kindMissing}
		if tags != "" {
			tagCell = kindCell{Text: tags, Title: tags, Sort: tags, Mono: true}
		}
		// A repository scan has no image tags; what it scanned is a commit.
		if snap.Source == repo.RepoScanSource && snap.Digest != nil {
			short := *snap.Digest
			if len(short) > 8 {
				short = short[:8]
			}
			tagCell = kindCell{Text: "commit " + short, Title: "Repository scan of commit " + *snap.Digest, Sort: short, Mono: true}
		}
		sev := badgeCell{Text: kindMissing, Tone: "none"}
		sevSort := ""
		switch {
		case snap.Total == 0:
			sev, sevSort = badgeCell{Text: "clean", Tone: "pass"}, "0"
		case snap.Severity != nil && *snap.Severity != "":
			sev = badgeCell{Text: strings.ToLower(*snap.Severity), Tone: "none"}
			sevSort = sortInt(-severityRank(*snap.Severity))
		}
		if snap.Total > 0 {
			sev.Tone = "pass"
			if snap.Critical+snap.High > 0 {
				sev.Tone = "fail"
			}
		}
		if row.Tone != "none" {
			sev.Tone = row.Tone // the collector's own verdict, the one the KPI counts
		}
		fp := falsePositives[bd.BindingID]
		fpCell := kindCell{Text: kindMissing, Sort: "0"}
		if fp > 0 {
			fpCell = kindCell{Text: sortInt(fp), Sort: sortInt(fp)}
		}
		pulls := kindCell{Text: kindMissing}
		if snap.Pulls != nil {
			pulls = kindCell{Text: thousands(int64(*snap.Pulls)), Sort: negInt(*snap.Pulls)}
		}
		row.Cells = []kindCell{
			tagCell,
			{Type: "badge", Badge: sev, Sort: sevSort},
			countCell(&snap.Critical, snap.Critical > 0),
			countCell(&snap.High, snap.High > 0),
			countCell(&snap.Medium, false),
			countCell(&snap.Low, false),
			countCell(&snap.Fixable, false),
			fpCell,
			timeCell(snap.ScanFinishedAt),
			pulls,
		}
	}
	has := reported > 0
	b.page.Stats = []kindStat{
		countStat("CRITICAL", crit, has, true),
		countStat("HIGH", high, has, true),
		countStat("MEDIUM", med, has, false),
		countStat("LOW", low, has, false),
		countStat("FIXABLE", fixable, has, false),
	}
	return b.finish(trivySeverityCol, func(i int) *time.Time { return syncedAt[i] })
}

// harborRepoURL opens a binding's repository in Harbor: the external key
// is "project/repository" (internal/collector/trivy's splitExternalKey),
// and Harbor's UI addresses it as /harbor/projects/{project}/repositories/
// {repository} — the legacy page's harborImageURL.
func harborRepoURL(base, key string) string {
	base = strings.TrimRight(base, "/")
	project, repoName, ok := strings.Cut(key, "/")
	if base == "" || !ok || project == "" || repoName == "" {
		return ""
	}
	return base + "/harbor/projects/" + url.PathEscape(project) + "/repositories/" + url.PathEscape(repoName)
}

// --- Sentry --------------------------------------------------------------

const sentryHealthCol = 0

func buildSentryKindPage(b *kindPageBase, envs []repo.SentryEnvironmentSummary, metrics map[uuid.UUID]repo.ComponentMetric) kindPage {
	b.setColumns([]kindColumn{
		{Label: "HEALTH", Width: "minmax(70px,.6fr)", Sort: "num", Title: "Grade and score of the worst environment — the one the project's Sentry card counts"},
		{Label: "ISSUES", Width: "minmax(48px,.45fr)", Sort: "num", Right: true, Title: "Unresolved issues, summed over environments"},
		{Label: "NEW", Width: "minmax(40px,.4fr)", Sort: "num", Right: true},
		{Label: "ESCALATING", Width: "minmax(70px,.55fr)", Sort: "num", Right: true},
		{Label: "REGRESSED", Width: "minmax(64px,.55fr)", Sort: "num", Right: true},
		{Label: "EVENTS", Width: "minmax(52px,.5fr)", Sort: "num", Right: true, Title: "Events across the unresolved issues"},
		{Label: "ENVS", Width: "minmax(40px,.35fr)", Sort: "num", Right: true, Title: "Environments in the latest sync"},
		{Label: "CHANGED", Width: "minmax(72px,.7fr)", Sort: "num", Title: "When Sentry's state for this binding last changed"},
	})
	byBinding := map[uuid.UUID][]repo.SentryEnvironmentSummary{}
	for _, e := range envs {
		byBinding[e.BindingID] = append(byBinding[e.BindingID], e)
	}
	var issues, newC, esc, reg, events int64
	reported := 0
	syncedAt := make([]*time.Time, len(b.page.Rows))
	for i := range b.page.Rows {
		row, bd := &b.page.Rows[i], b.bindings[i]
		got := byBinding[bd.BindingID]
		row.ProviderURL = sentryIssuesURL(bd, got)
		if len(got) == 0 {
			row.Cells = missingCells(b.page.Columns, sentryHealthCol)
			continue
		}
		reported++
		var tIssues, tNew, tEsc, tReg, tEvents int
		changed := got[0].CapturedAt
		synced := got[0].CollectedAt
		worst := got[0]
		for _, e := range got {
			tIssues += e.TotalIssues
			tNew += e.NewCount
			tEsc += e.EscalatingCount
			tReg += e.RegressedCount
			tEvents += e.Events
			if e.CapturedAt.After(changed) {
				changed = e.CapturedAt
			}
			if e.Score != nil && (worst.Score == nil || *e.Score < *worst.Score) {
				worst = e
			}
		}
		syncedAt[i] = &synced
		issues, newC, esc, reg, events = issues+int64(tIssues), newC+int64(tNew), esc+int64(tEsc), reg+int64(tReg), events+int64(tEvents)

		// The grade the KPI card counts is the binding's headline: the
		// worst environment as the collector judged it. The snapshots'
		// own lowest score stands in only before a headline exists.
		grade, score := "", worst.Score
		if worst.Grade != nil {
			grade = *worst.Grade
		}
		tone := "none"
		if grade != "" {
			tone = sentryGradeTone(grade)
		}
		if kh, ok := metrics[bd.ComponentID].Metrics()["sentry"]; ok {
			grade, score, tone = kh.Grade, kh.Score, row.Tone
		}
		health := badgeCell{Text: kindMissing, Tone: tone}
		healthSort := ""
		switch {
		case grade != "" && score != nil:
			health.Text = fmt.Sprintf("%s · %d", grade, *score)
		case grade != "":
			health.Text = grade
		case score != nil:
			health.Text = sortInt(*score)
		}
		if score != nil {
			healthSort = sortInt(*score)
		}
		envCount := len(got)
		row.Cells = []kindCell{
			{Type: "badge", Badge: health, Sort: healthSort},
			countCell(&tIssues, false),
			countCell(&tNew, false),
			countCell(&tEsc, tEsc > 0),
			countCell(&tReg, tReg > 0),
			countCell(&tEvents, false),
			{Text: sortInt(envCount), Sort: sortInt(envCount), Title: sentryEnvNames(got)},
			timeCell(&changed),
		}
	}
	has := reported > 0
	b.page.Stats = []kindStat{
		countStat("OPEN ISSUES", issues, has, false),
		countStat("NEW", newC, has, false),
		countStat("ESCALATING", esc, has, true),
		countStat("REGRESSED", reg, has, true),
		countStat("EVENTS", events, has, false),
	}
	return b.finish(sentryHealthCol, func(i int) *time.Time { return syncedAt[i] })
}

func sentryEnvNames(envs []repo.SentryEnvironmentSummary) string {
	names := make([]string, 0, len(envs))
	for _, e := range envs {
		names = append(names, e.Environment)
	}
	return strings.Join(names, ", ")
}

// sentryIssuesURL opens a binding's issues in Sentry — the legacy page's
// sentryIssuesURL. The issue stream filters by the numeric project id,
// known once a sync has run, and carries the sync's own search (the
// unresolved filter and, for a shared project, the service tag), so it
// opens this component's issues rather than the whole project's. Before
// any sync it falls back to the project page by slug. The organization is
// the one the sync recorded, else the connection's configured one.
func sentryIssuesURL(bd repo.ProjectBinding, envs []repo.SentryEnvironmentSummary) string {
	base := strings.TrimRight(bd.ConnectionURL, "/")
	var cfg struct {
		Org string `json:"org"`
	}
	_ = json.Unmarshal(bd.ConnectionConfig, &cfg)
	org, slug := cfg.Org, bd.ExternalKey
	var projectID int64
	var query string
	if len(envs) > 0 {
		e := envs[0]
		if e.SentryOrg != "" {
			org = e.SentryOrg
		}
		if e.ProjectSlug != "" {
			slug = e.ProjectSlug
		}
		projectID, query = e.SentryProjectID, e.StatusFilter
	}
	if base == "" || org == "" || slug == "" {
		return ""
	}
	if projectID <= 0 {
		return fmt.Sprintf("%s/organizations/%s/projects/%s/", base, url.PathEscape(org), url.PathEscape(slug))
	}
	q := url.Values{}
	q.Set("project", fmt.Sprintf("%d", projectID))
	q.Set("statsPeriod", "14d")
	if query != "" {
		q.Set("query", query)
	}
	return fmt.Sprintf("%s/organizations/%s/issues/?%s", base, url.PathEscape(org), q.Encode())
}

// --- GitLab --------------------------------------------------------------

const gitlabPipelineCol = 3

func buildGitlabKindPage(b *kindPageBase, snaps []repo.GitlabSnapshot) kindPage {
	b.setColumns([]kindColumn{
		{Label: "LANGUAGE", Width: "minmax(52px,.6fr)", Sort: "text"},
		{Label: "DEFAULT", Width: "minmax(52px,.6fr)", Sort: "text", Title: "Default branch"},
		{Label: "BRANCHES", Width: "minmax(108px,.7fr)", Sort: "num", Right: true, Title: "All branches; active and stale beneath"},
		{Label: "PIPELINE", Width: "minmax(100px,.9fr)", Sort: "num", Title: "The default branch's last pipeline"},
		{Label: "SUCCESS", Width: "minmax(78px,.6fr)", Sort: "num", Right: true, Title: "Pipeline success rate over the recent sample"},
		{Label: "LAST ACTIVITY", Width: "minmax(84px,.8fr)", Sort: "num"},
		{Label: "COMMITS", Width: "minmax(52px,.55fr)", Sort: "num", Right: true, Title: "Commits in the last 30 days"},
		{Label: "GOVERNANCE", Width: "minmax(90px,.85fr)", Title: "Default-branch protection, visibility, archived"},
	})
	bySnap := make(map[uuid.UUID]repo.GitlabSnapshot, len(snaps))
	for _, s := range snaps {
		bySnap[s.BindingID] = s
	}
	var failing, active, stale, protected int64
	reported := 0
	syncedAt := make([]*time.Time, len(b.page.Rows))
	for i := range b.page.Rows {
		row, bd := &b.page.Rows[i], b.bindings[i]
		if base := strings.TrimRight(bd.ConnectionURL, "/"); base != "" {
			row.ProviderURL = base + "/" + bd.ExternalKey
		}
		snap, ok := bySnap[bd.BindingID]
		if !ok {
			row.Cells = missingCells(b.page.Columns, gitlabPipelineCol)
			continue
		}
		reported++
		syncedAt[i] = &snap.CollectedAt
		v := buildGitlabDetailView(&snap)
		if v.WebURL != "" {
			row.ProviderURL = v.WebURL
		}
		active += int64(v.BranchesActive)
		stale += int64(v.BranchesStale)
		if v.Protected {
			protected++
		}
		if v.PipelineStatus == "failed" {
			failing++
		}

		lang := kindCell{Text: kindMissing}
		if v.Language != "" {
			lang = kindCell{Text: v.Language, Sort: v.Language}
		}
		def := kindCell{Text: v.DefaultBranch, Mono: true}
		if v.DefaultBranch != kindMissing {
			def.Sort = v.DefaultBranch
		}
		branches := kindCell{Text: kindMissing}
		if snap.BranchesTotal != nil {
			branches = kindCell{Text: sortInt(v.BranchesTotal), Sort: negInt(v.BranchesTotal),
				Sub: fmt.Sprintf("%d active · %d stale", v.BranchesActive, v.BranchesStale)}
		}
		pipe := badgeCell{Text: kindMissing, Tone: v.PipelineTone}
		pipeSort := ""
		switch v.PipelineStatus {
		case "":
		case "success":
			pipe.Text, pipeSort = "passed", "1"
		case "failed":
			pipe.Text, pipeSort = "failed", "0"
		default:
			pipe.Text = v.PipelineStatus
		}
		pipeCell := kindCell{Type: "badge", Badge: pipe, Sort: pipeSort, Href: v.PipelineWebURL}
		if v.LastDuration != kindMissing || v.PipelineFinished != kindMissing {
			pipeCell.Sub = v.LastDuration + " · " + v.PipelineFinished
		}
		success := kindCell{Text: v.SuccessRate}
		if snap.PipelineSuccessRate != nil && v.SuccessRate != kindMissing {
			success.Sort = sortFloat(*snap.PipelineSuccessRate)
		}
		activity := timeCell(snap.LastActivityAt)
		if v.LastCommitter != "" {
			activity.Sub = "by " + v.LastCommitter
			activity.Title += " · last commit by " + v.LastCommitter
		}
		commits := kindCell{Text: v.Commits30d}
		if snap.Commits30d != nil {
			commits.Sort = sortInt(*snap.Commits30d)
		}
		row.Cells = []kindCell{
			lang, def, branches, pipeCell, success, activity, commits,
			{Type: "governance", Protected: v.Protected, Visibility: v.Visibility, Archived: v.Archived},
		}
	}
	has := reported > 0
	prot := kindStat{Label: "PROTECTED DEFAULT", Value: kindMissing, Tone: "none"}
	if has {
		prot.Value = fmt.Sprintf("%d/%d", protected, reported)
		prot.Sub = "default branch protected"
		prot.Tone = "pass"
		if protected < int64(reported) {
			prot.Tone = "fail"
		}
	}
	b.page.Stats = []kindStat{
		countStat("FAILING PIPELINES", failing, has, true),
		countStat("ACTIVE BRANCHES", active, has, false),
		countStat("STALE BRANCHES", stale, has, false),
		prot,
	}
	return b.finish(gitlabPipelineCol, func(i int) *time.Time { return syncedAt[i] })
}
