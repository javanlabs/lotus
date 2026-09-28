// Package legacylotus imports the metric history of the legacy Lotus
// (lotus.alurkerja.com, a SQLite database) into this one: its SonarQube,
// GitLab and Trivy snapshots, each at the time it was taken, through the
// same collector Persist and facts writers live collection uses.
//
// The legacy database is read on its own server by
// deploy/legacy-lotus-export.py, which writes JSON Lines: a header
// {"kind":"meta","exported_at":…}, then one row per snapshot, oldest
// first. Sentry is not imported: the legacy app kept only its latest
// state (one row per project and environment, replaced on every sync),
// which live collection reproduces on its first run.
package legacylotus

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	glclient "github.com/uyab/lotus/internal/clients/gitlab"
	"github.com/uyab/lotus/internal/clients/sonar"
	"github.com/uyab/lotus/internal/collector"
	gitlabcollector "github.com/uyab/lotus/internal/collector/gitlab"
	sonarcollector "github.com/uyab/lotus/internal/collector/sonarqube"
	trivycollector "github.com/uyab/lotus/internal/collector/trivy"
	"github.com/uyab/lotus/internal/repo"
	"github.com/uyab/lotus/internal/retention"
	"github.com/uyab/lotus/internal/tenant"
	"github.com/uyab/lotus/internal/timeseries"
)

// Options configures Import.
type Options struct {
	// DryRun reads and maps every row and reports what would happen,
	// writing nothing.
	DryRun bool
	// Progress, when set, is called after each finished month.
	Progress func(month time.Time, imported int)
}

// Report is what an import did (or, dry, would do).
type Report struct {
	ExportedAt      time.Time
	Read            int      // snapshot rows in the export
	Imported        int      // written (new snapshots and seen-again ones)
	AlreadyImported int      // at or before what a binding already holds
	Unusable        int      // rows a collector refuses (no measures at all)
	UnknownServices []string // no component carries this legacy name
	UnknownImages   []string // Harbor images with no component mapping
	MissingBindings []string // "service/kind": the component has no such binding
	From, To        time.Time
}

// row is one line of the export; which fields are set depends on Kind.
type row struct {
	Kind       string `json:"kind"`
	ExportedAt string `json:"exported_at"`
	Service    string `json:"service"`
	Repo       string `json:"repo"`
	Image      string `json:"image"`
	CapturedAt string `json:"captured_at"`

	// sonar
	QualityGate            *string  `json:"quality_gate"`
	Coverage               *float64 `json:"coverage"`
	Bugs                   *int     `json:"bugs"`
	Vulnerabilities        *int     `json:"vulnerabilities"`
	CodeSmells             *int     `json:"code_smells"`
	SecurityHotspots       *int     `json:"security_hotspots"`
	DuplicatedLinesDensity *float64 `json:"duplicated_lines_density"`
	NCLOC                  *int     `json:"ncloc"`
	Complexity             *int     `json:"complexity"`
	ReliabilityRating      *string  `json:"reliability_rating"`
	SecurityRating         *string  `json:"security_rating"`
	SqaleRating            *string  `json:"sqale_rating"`
	SqaleIndexMin          *int     `json:"sqale_index_min"`
	TestExecutionTimeMs    *int     `json:"test_execution_time_ms"`
	Languages              *string  `json:"languages"`
	LastScanAt             *string  `json:"last_scan_at"`

	// gitlab
	PrimaryLanguage         *string  `json:"primary_language"`
	LanguagesJSON           *string  `json:"languages_json"`
	DefaultBranch           *string  `json:"default_branch"`
	BranchesTotal           *int     `json:"branches_total"`
	BranchesActive          *int     `json:"branches_active"`
	BranchesStale           *int     `json:"branches_stale"`
	LastPipelineStatus      *string  `json:"last_pipeline_status"`
	LastPipelineID          *int     `json:"last_pipeline_id"`
	LastPipelineDurationSec *int     `json:"last_pipeline_duration_sec"`
	LastPipelineWebURL      *string  `json:"last_pipeline_web_url"`
	LastPipelineFinishedAt  *string  `json:"last_pipeline_finished_at"`
	LastPipelineRef         *string  `json:"last_pipeline_ref"`
	PipelineSuccessRate     *float64 `json:"pipeline_success_rate"`
	PipelineSampleSize      *int     `json:"pipeline_sample_size"`
	LastActivityAt          *string  `json:"last_activity_at"`
	Commits30d              *int     `json:"commits_30d"`
	Commits30dCapped        *int     `json:"commits_30d_capped"`
	DefaultBranchProtected  *int     `json:"default_branch_protected"`
	Visibility              *string  `json:"visibility"`
	Archived                *int     `json:"archived"`
	WebURL                  *string  `json:"web_url"`
	LastCommitterName       *string  `json:"last_committer_name"`

	// trivy
	Digest         *string `json:"digest"`
	Tags           *string `json:"tags"`
	Severity       *string `json:"severity"`
	Total          *int    `json:"total"`
	Fixable        *int    `json:"fixable"`
	Critical       *int    `json:"critical"`
	High           *int    `json:"high"`
	Medium         *int    `json:"medium"`
	Low            *int    `json:"low"`
	Unknown        *int    `json:"unknown"`
	Pulls          *int    `json:"pulls"`
	ArtifactCount  *int    `json:"artifact_count"`
	ImagePushedAt  *string `json:"image_pushed_at"`
	ScanFinishedAt *string `json:"scan_finished_at"`
}

// parseTime reads the legacy app's timestamps: SQLite DATETIME text in
// Go's default time format ("2026-05-05 03:58:48.413122189+00:00") or
// RFC 3339; no offset means UTC. Truncated to Postgres's microseconds.
func parseTime(s string) (time.Time, error) {
	for _, layout := range []string{
		"2006-01-02 15:04:05.999999999-07:00", time.RFC3339Nano,
		"2006-01-02 15:04:05.999999999 -0700 MST", "2006-01-02 15:04:05.999999999",
	} {
		if t, err := time.Parse(layout, strings.TrimSpace(s)); err == nil {
			// Postgres keeps microseconds; a nanosecond legacy time would
			// otherwise read as later than itself once stored, and a
			// resumed import would take it for new.
			return t.UTC().Truncate(time.Microsecond), nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognized time %q", s)
}

func optTime(s *string) *time.Time {
	if s == nil || *s == "" {
		return nil
	}
	t, err := parseTime(*s)
	if err != nil {
		return nil
	}
	return &t
}

func str(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func num(v *int) int {
	if v == nil {
		return 0
	}
	return *v
}

// bindingKey names a binding the import writes to.
type bindingKey struct {
	kind, externalKey string
	component         uuid.UUID
}

type importer struct {
	app, ddl   *sqlx.DB
	s          tenant.Scope
	opts       Options
	rep        Report
	components map[string]uuid.UUID // legacy name, and slug, -> component
	bindings   map[bindingKey]uuid.UUID
	highwater  map[uuid.UUID]time.Time // binding -> last observation already held
	harborConn uuid.UUID
	unknownSvc map[string]bool
	unknownImg map[string]bool
	missing    map[string]bool
	month      time.Time // the month being imported (UTC)
	monthFrom  time.Time // first and last observation imported in it
	monthTo    time.Time
	monthCount int
}

// Import reads an export from r and writes its history into the
// organization orgID. app is the RLS-restricted application connection;
// ddl can create partitions and roll up (the migrate role, as the
// retention and timeseries jobs use). Rows at or before what a binding
// already holds (up to the export time) are skipped, so an interrupted
// import is resumed by running it again.
func Import(ctx context.Context, app, ddl *sqlx.DB, orgID uuid.UUID, r io.Reader, opts Options) (Report, error) {
	im := &importer{
		app: app, ddl: ddl, s: tenant.FromRun(app, orgID), opts: opts,
		components: map[string]uuid.UUID{}, bindings: map[bindingKey]uuid.UUID{}, highwater: map[uuid.UUID]time.Time{},
		unknownSvc: map[string]bool{}, unknownImg: map[string]bool{}, missing: map[string]bool{},
	}
	if err := im.load(ctx); err != nil {
		return im.rep, err
	}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	line := 0
	for sc.Scan() {
		line++
		if strings.TrimSpace(sc.Text()) == "" {
			continue
		}
		var rw row
		if err := json.Unmarshal(sc.Bytes(), &rw); err != nil {
			return im.rep, fmt.Errorf("line %d: %w", line, err)
		}
		if rw.Kind == "meta" {
			t, err := parseTime(rw.ExportedAt)
			if err != nil {
				return im.rep, fmt.Errorf("line %d: exported_at: %w", line, err)
			}
			im.rep.ExportedAt = t
			continue
		}
		if im.rep.ExportedAt.IsZero() {
			return im.rep, errors.New("the export has no meta line with exported_at (was it made by deploy/legacy-lotus-export.py?)")
		}
		at, err := parseTime(rw.CapturedAt)
		if err != nil {
			return im.rep, fmt.Errorf("line %d: captured_at: %w", line, err)
		}
		im.rep.Read++
		if im.rep.From.IsZero() || at.Before(im.rep.From) {
			im.rep.From = at
		}
		if at.After(im.rep.To) {
			im.rep.To = at
		}
		if err := im.row(ctx, rw, at); err != nil {
			return im.rep, fmt.Errorf("line %d (%s at %s): %w", line, rw.Kind, rw.CapturedAt, err)
		}
	}
	if err := sc.Err(); err != nil {
		return im.rep, err
	}
	if err := im.finishMonth(ctx); err != nil {
		return im.rep, err
	}
	im.rep.UnknownServices = sortedKeys(im.unknownSvc)
	im.rep.UnknownImages = sortedKeys(im.unknownImg)
	im.rep.MissingBindings = sortedKeys(im.missing)
	return im.rep, nil
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// load reads the organization's components, bindings and Harbor
// connection once.
func (im *importer) load(ctx context.Context) error {
	return im.s.Tx(ctx, func(tx *sqlx.Tx) error {
		var comps []struct {
			ID         uuid.UUID `db:"id"`
			Slug       string    `db:"slug"`
			LegacyName *string   `db:"legacy_name"`
		}
		if err := tx.SelectContext(ctx, &comps, `SELECT id, slug, legacy_name FROM components WHERE archived_at IS NULL`); err != nil {
			return err
		}
		for _, c := range comps {
			im.components[c.Slug] = c.ID
		}
		for _, c := range comps { // the legacy name wins over a slug
			if c.LegacyName != nil && *c.LegacyName != "" {
				im.components[*c.LegacyName] = c.ID
			}
		}
		var binds []struct {
			ID          uuid.UUID `db:"id"`
			Component   uuid.UUID `db:"component_id"`
			Kind        string    `db:"kind"`
			ExternalKey string    `db:"external_key"`
		}
		if err := tx.SelectContext(ctx, &binds, `SELECT id, component_id, kind, external_key FROM metric_bindings WHERE deleted_at IS NULL`); err != nil {
			return err
		}
		for _, b := range binds {
			im.bindings[bindingKey{b.Kind, b.ExternalKey, b.Component}] = b.ID
			if b.Kind != "trivy" { // one sonar and one gitlab binding per component
				im.bindings[bindingKey{b.Kind, "", b.Component}] = b.ID
			}
		}
		err := tx.GetContext(ctx, &im.harborConn, `
			SELECT id FROM integration_connections WHERE provider = 'harbor_trivy' ORDER BY created_at LIMIT 1`)
		if err != nil && !isNoRows(err) {
			return err
		}
		return nil
	})
}

func isNoRows(err error) bool { return err != nil && strings.Contains(err.Error(), "no rows") }

// binding is the binding a row writes to, created for a Trivy repository
// the first time it is seen; uuid.Nil when there is none.
func (im *importer) binding(ctx context.Context, rw row) (uuid.UUID, uuid.UUID, error) {
	switch rw.Kind {
	case "sonar", "gitlab":
		comp, ok := im.components[rw.Service]
		if !ok {
			im.unknownSvc[rw.Service] = true
			return uuid.Nil, uuid.Nil, nil
		}
		kind := map[string]string{"sonar": "sonarqube", "gitlab": "gitlab"}[rw.Kind]
		id, ok := im.bindings[bindingKey{kind, "", comp}]
		if !ok {
			im.missing[rw.Service+"/"+kind] = true
			return uuid.Nil, uuid.Nil, nil
		}
		return comp, id, nil
	case "trivy":
		slug, ok := TrivyComponentSlug(rw.Image)
		comp, found := im.components[slug]
		if !ok || !found {
			im.unknownImg[rw.Image] = true
			return uuid.Nil, uuid.Nil, nil
		}
		key := bindingKey{"trivy", rw.Repo, comp}
		if id, ok := im.bindings[key]; ok {
			return comp, id, nil
		}
		if im.harborConn == uuid.Nil {
			im.missing[rw.Image+"/trivy (no Harbor connection)"] = true
			return uuid.Nil, uuid.Nil, nil
		}
		if im.opts.DryRun {
			return comp, uuid.Nil, nil // would be created
		}
		var id uuid.UUID
		if err := im.s.Tx(ctx, func(tx *sqlx.Tx) error {
			return tx.GetContext(ctx, &id, `
				INSERT INTO metric_bindings (org_id, component_id, connection_id, kind, external_key, config)
				VALUES ($1, $2, $3, 'trivy', $4, '{}')
				ON CONFLICT (component_id, kind, external_key) WHERE deleted_at IS NULL
				DO UPDATE SET enabled = true
				RETURNING id`, im.s.OrgID(), comp, im.harborConn, rw.Repo)
		}); err != nil {
			return uuid.Nil, uuid.Nil, err
		}
		im.bindings[key] = id
		return comp, id, nil
	}
	return uuid.Nil, uuid.Nil, fmt.Errorf("unknown kind %q", rw.Kind)
}

// held is the last observation a binding already holds, up to the export
// time — anything later is live collection's, not a previous import's.
func (im *importer) held(ctx context.Context, kind string, bindingID uuid.UUID) (time.Time, error) {
	if t, ok := im.highwater[bindingID]; ok {
		return t, nil
	}
	table := map[string]string{"sonar": "sonar_snapshots", "gitlab": "gitlab_snapshots", "trivy": "trivy_snapshots"}[kind]
	var t *time.Time
	err := im.s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.GetContext(ctx, &t, fmt.Sprintf(
			`SELECT max(collected_at) FROM %s WHERE binding_id = $1 AND collected_at <= $2`, table), bindingID, im.rep.ExportedAt)
	})
	if err != nil {
		return time.Time{}, err
	}
	var out time.Time
	if t != nil {
		out = *t
	}
	im.highwater[bindingID] = out
	return out, nil
}

func (im *importer) row(ctx context.Context, rw row, at time.Time) error {
	comp, bindingID, err := im.binding(ctx, rw)
	if err != nil || comp == uuid.Nil {
		return err
	}
	result, err := buildResult(rw)
	if err != nil {
		im.rep.Unusable++
		return nil
	}
	if im.opts.DryRun {
		im.rep.Imported++
		return nil
	}
	if !at.After(mustHeld(im.held(ctx, rw.Kind, bindingID))) {
		im.rep.AlreadyImported++
		return nil
	}
	if err := im.enterMonth(ctx, at); err != nil {
		return err
	}
	octx := repo.WithObservedAt(ctx, at)
	var persist func(context.Context, tenant.Scope, uuid.UUID, uuid.UUID, collector.Result) error
	kind := rw.Kind
	switch rw.Kind {
	case "sonar":
		persist, kind = sonarcollector.Persist, "sonarqube"
	case "gitlab":
		persist = gitlabcollector.Persist
	case "trivy":
		persist = trivycollector.Persist
	}
	if err := persist(octx, im.s, comp, bindingID, result); err != nil {
		return err
	}
	if err := repo.InsertMetricFacts(octx, im.s, comp, bindingID, kind, result); err != nil {
		return err
	}
	im.highwater[bindingID] = at
	im.rep.Imported++
	im.monthCount++
	if im.monthFrom.IsZero() || at.Before(im.monthFrom) {
		im.monthFrom = at
	}
	if at.After(im.monthTo) {
		im.monthTo = at
	}
	return nil
}

// mustHeld hides held's error on the dry path, where it is never called.
func mustHeld(t time.Time, err error) time.Time {
	if err != nil {
		panic(err)
	}
	return t
}

// enterMonth makes sure at's month has its partitions, and rolls up the
// month before once the import has moved past it — before the live
// server's own passes can age its raw facts out.
func (im *importer) enterMonth(ctx context.Context, at time.Time) error {
	month := time.Date(at.Year(), at.Month(), 1, 0, 0, 0, 0, time.UTC)
	if month.Equal(im.month) {
		return nil
	}
	if err := im.finishMonth(ctx); err != nil {
		return err
	}
	if err := retention.EnsurePartitions(ctx, im.ddl, month, 1); err != nil {
		return err
	}
	if err := timeseries.EnsurePartitions(ctx, im.ddl, month, 1); err != nil {
		return err
	}
	im.month, im.monthFrom, im.monthTo, im.monthCount = month, time.Time{}, time.Time{}, 0
	return nil
}

// finishMonth rolls the month just imported up into the hourly and daily
// tiers the charts read for ranges wider than the raw tier keeps.
func (im *importer) finishMonth(ctx context.Context) error {
	if im.month.IsZero() || im.monthCount == 0 {
		return nil
	}
	to := im.monthTo.Add(time.Hour)
	if err := timeseries.BackfillHourly(ctx, im.ddl, im.monthFrom, to); err != nil {
		return err
	}
	if err := timeseries.BackfillDaily(ctx, im.ddl, im.monthFrom, to); err != nil {
		return err
	}
	if im.opts.Progress != nil {
		im.opts.Progress(im.month, im.monthCount)
	}
	return nil
}

// buildResult is the collector Result a live collect of the same data
// would have produced.
func buildResult(rw row) (collector.Result, error) {
	switch rw.Kind {
	case "sonar":
		return sonarcollector.BuildImportedResult(&sonar.Measures{
			QualityGate: str(rw.QualityGate), Coverage: rw.Coverage, Bugs: rw.Bugs, Vulnerabilities: rw.Vulnerabilities,
			CodeSmells: rw.CodeSmells, SecurityHotspots: rw.SecurityHotspots, DuplicatedLinesDensity: rw.DuplicatedLinesDensity,
			NCLOC: rw.NCLOC, Complexity: rw.Complexity, ReliabilityRating: str(rw.ReliabilityRating),
			SecurityRating: str(rw.SecurityRating), SqaleRating: str(rw.SqaleRating), SqaleIndexMin: rw.SqaleIndexMin,
			TestExecutionTimeMs: rw.TestExecutionTimeMs, Languages: str(rw.Languages), LastScan: optTime(rw.LastScanAt),
		})
	case "gitlab":
		return gitlabcollector.BuildImportedResult(&glclient.Health{
			PrimaryLanguage: str(rw.PrimaryLanguage), LanguagesJSON: str(rw.LanguagesJSON), DefaultBranch: str(rw.DefaultBranch),
			BranchesTotal: num(rw.BranchesTotal), BranchesActive: num(rw.BranchesActive), BranchesStale: num(rw.BranchesStale),
			DefaultBranchProtected: num(rw.DefaultBranchProtected) != 0, Visibility: str(rw.Visibility), Archived: num(rw.Archived) != 0,
			LastActivityAt: optTime(rw.LastActivityAt), WebURL: str(rw.WebURL),
			Commits30d: num(rw.Commits30d), Commits30dCapped: num(rw.Commits30dCapped) != 0, LastCommitterName: str(rw.LastCommitterName),
			LastPipelineStatus: str(rw.LastPipelineStatus), LastPipelineID: rw.LastPipelineID, LastPipelineDurationSec: rw.LastPipelineDurationSec,
			LastPipelineWebURL: str(rw.LastPipelineWebURL), LastPipelineFinishedAt: optTime(rw.LastPipelineFinishedAt),
			LastPipelineRef: str(rw.LastPipelineRef), PipelineSuccessRate: rw.PipelineSuccessRate, PipelineSampleSize: num(rw.PipelineSampleSize),
		})
	case "trivy":
		var tags []string
		for _, t := range strings.Split(str(rw.Tags), ",") {
			if t = strings.TrimSpace(t); t != "" {
				tags = append(tags, t)
			}
		}
		return trivycollector.BuildImportedResult(trivycollector.ImportedSnapshot{
			Digest: str(rw.Digest), Tags: tags, Severity: str(rw.Severity),
			Counts: trivycollector.Counts{Total: num(rw.Total), Fixable: num(rw.Fixable), Critical: num(rw.Critical),
				High: num(rw.High), Medium: num(rw.Medium), Low: num(rw.Low), Unknown: num(rw.Unknown)},
			Pulls: rw.Pulls, ArtifactCount: rw.ArtifactCount,
			ImagePushedAt: optTime(rw.ImagePushedAt), ScanFinishedAt: optTime(rw.ScanFinishedAt),
		}, "critical")
	}
	return collector.Result{}, fmt.Errorf("unknown kind %q", rw.Kind)
}
