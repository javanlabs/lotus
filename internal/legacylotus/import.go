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
	lotusdb "github.com/uyab/lotus/internal/db"
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
	AlreadyImported int      // at or before what an earlier run imported
	AfterLive       int      // at or after the binding\'s first live observation
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

// progress is where a binding's legacy history stands: imported up to
// importedTo (legacy_import_progress), and live collection's first
// observation, if any — legacy rows at or after it are left out, so the
// two never overlap.
type progress struct {
	importedTo time.Time
	liveFrom   *time.Time
}

type importer struct {
	app, ddl   *sqlx.DB
	s          tenant.Scope
	opts       Options
	rep        Report
	components map[string]uuid.UUID // legacy name, and slug, -> component
	bindings   map[bindingKey]uuid.UUID
	progress   map[uuid.UUID]*progress
	harborConn uuid.UUID
	unknownSvc map[string]bool
	unknownImg map[string]bool
	missing    map[string]bool
	oldestOK   bool      // the retention window was checked against the oldest row
	month      time.Time // the calendar month being imported (UTC)
}

// Import reads an export from r and writes its history into the
// organization orgID. app is the RLS-restricted application connection;
// ddl can create partitions and roll up (the migrate role, as the
// retention and timeseries jobs use). It holds the legacy-import lock
// (LockImport) for the whole run, so those jobs leave partitions alone,
// and rolls every calendar month it touches up whole, so an interrupted
// import is resumed by running it again.
func Import(ctx context.Context, app, ddl *sqlx.DB, orgID uuid.UUID, r io.Reader, opts Options) (Report, error) {
	im := &importer{
		app: app, ddl: ddl, s: tenant.FromRun(app, orgID), opts: opts,
		components: map[string]uuid.UUID{}, bindings: map[bindingKey]uuid.UUID{}, progress: map[uuid.UUID]*progress{},
		unknownSvc: map[string]bool{}, unknownImg: map[string]bool{}, missing: map[string]bool{},
	}
	if !opts.DryRun {
		release, err := LockImport(ctx, ddl)
		if err != nil {
			return im.rep, err
		}
		defer release()
	}
	if err := im.load(ctx); err != nil {
		return im.rep, err
	}
	err := im.read(ctx, r)
	// Roll up the month in hand even when the read failed part way: its
	// raw facts would otherwise age out before a rerun reaches it.
	if ferr := im.finishMonth(ctx); err == nil {
		err = ferr
	}
	im.rep.UnknownServices = sortedKeys(im.unknownSvc)
	im.rep.UnknownImages = sortedKeys(im.unknownImg)
	im.rep.MissingBindings = sortedKeys(im.missing)
	return im.rep, err
}

// LockImport takes the legacy-import advisory lock on a connection of its
// own and returns its release; it fails at once if another import holds it.
func LockImport(ctx context.Context, ddl *sqlx.DB) (func(), error) {
	conn, err := ddl.Connx(ctx)
	if err != nil {
		return nil, err
	}
	var got bool
	if err := conn.GetContext(ctx, &got, `SELECT pg_try_advisory_lock($1, $2)`, lotusdb.LegacyImportLockClass, lotusdb.LegacyImportLockID); err != nil {
		conn.Close()
		return nil, err
	}
	if !got {
		conn.Close()
		return nil, errors.New("another legacy import is running")
	}
	return func() {
		_, _ = conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock($1, $2)`, lotusdb.LegacyImportLockClass, lotusdb.LegacyImportLockID)
		conn.Close()
	}, nil
}

func (im *importer) read(ctx context.Context, r io.Reader) error {
	ctx = repo.WithMetricKeyMemo(ctx)
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
			return fmt.Errorf("line %d: %w", line, err)
		}
		if rw.Kind == "meta" {
			t, err := parseTime(rw.ExportedAt)
			if err != nil {
				return fmt.Errorf("line %d: exported_at: %w", line, err)
			}
			im.rep.ExportedAt = t
			continue
		}
		if im.rep.ExportedAt.IsZero() {
			return errors.New("the export has no meta line with exported_at (was it made by deploy/legacy-lotus-export.py?)")
		}
		at, err := parseTime(rw.CapturedAt)
		if err != nil {
			return fmt.Errorf("line %d: captured_at: %w", line, err)
		}
		if err := im.checkRetention(ctx, at); err != nil {
			return err
		}
		im.rep.Read++
		if im.rep.From.IsZero() || at.Before(im.rep.From) {
			im.rep.From = at
		}
		if at.After(im.rep.To) {
			im.rep.To = at
		}
		if err := im.row(ctx, rw, at); err != nil {
			return fmt.Errorf("line %d (%s at %s): %w", line, rw.Kind, rw.CapturedAt, err)
		}
	}
	return sc.Err()
}

// checkRetention refuses history the retention pass would drop: the
// export is oldest first, so its first row decides.
func (im *importer) checkRetention(ctx context.Context, oldest time.Time) error {
	if im.oldestOK {
		return nil
	}
	age := retention.HeadlineAge(ctx, im.ddl)
	if age <= 0 {
		return errors.New("could not read the organizations' snapshot retention (retention_days)")
	}
	if need := time.Since(oldest); need > age {
		return fmt.Errorf("the snapshot retention window (%d days) does not reach back to %s: set Settings → Organization → Snapshot retention to at least %d days first, or the next retention pass drops the imported history",
			int(age.Hours()/24), oldest.Format("2006-01-02"), int(need.Hours()/24)+1)
	}
	im.oldestOK = true
	return nil
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
// the first time it is seen (not on a dry run); uuid.Nil component when
// there is none.
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
			// An existing binding is reused as it is — enabled or not is the
			// admin's call, not the import's.
			return tx.GetContext(ctx, &id, `
				INSERT INTO metric_bindings (org_id, component_id, connection_id, kind, external_key, config)
				VALUES ($1, $2, $3, 'trivy', $4, '{}')
				ON CONFLICT (component_id, kind, external_key) WHERE deleted_at IS NULL
				DO UPDATE SET enabled = metric_bindings.enabled
				RETURNING id`, im.s.OrgID(), comp, im.harborConn, rw.Repo)
		}); err != nil {
			return uuid.Nil, uuid.Nil, err
		}
		im.bindings[key] = id
		return comp, id, nil
	}
	return uuid.Nil, uuid.Nil, fmt.Errorf("unknown kind %q", rw.Kind)
}

var snapshotTable = map[string]string{"sonar": "sonar_snapshots", "gitlab": "gitlab_snapshots", "trivy": "trivy_snapshots"}

// progressOf reads a binding's progress the first time it is met. The
// first live observation is the earliest snapshot after what was imported
// — except one captured exactly at the row in hand, which is the import's
// own write from a run cut off before it could record its progress.
func (im *importer) progressOf(ctx context.Context, kind string, bindingID uuid.UUID, at time.Time) (*progress, error) {
	if p, ok := im.progress[bindingID]; ok {
		return p, nil
	}
	p := &progress{}
	if bindingID != uuid.Nil { // a Trivy binding a dry run has not made yet
		err := im.s.Tx(ctx, func(tx *sqlx.Tx) error {
			err := tx.GetContext(ctx, &p.importedTo, `SELECT imported_to FROM legacy_import_progress WHERE binding_id = $1`, bindingID)
			if err != nil && !isNoRows(err) {
				return err
			}
			return tx.GetContext(ctx, &p.liveFrom, fmt.Sprintf(`
				SELECT min(captured_at) FROM %s WHERE binding_id = $1 AND captured_at > $2 AND captured_at <> $3`,
				snapshotTable[kind]), bindingID, p.importedTo, at)
		})
		if err != nil {
			return nil, err
		}
	}
	im.progress[bindingID] = p
	return p, nil
}

func (im *importer) row(ctx context.Context, rw row, at time.Time) error {
	comp, bindingID, err := im.binding(ctx, rw)
	if err != nil || comp == uuid.Nil {
		return err
	}
	if !im.opts.DryRun {
		// Every month the export holds is rolled up whole, even one whose
		// rows were all imported by an earlier, interrupted run.
		if err := im.enterMonth(ctx, at); err != nil {
			return err
		}
	}
	p, err := im.progressOf(ctx, rw.Kind, bindingID, at)
	if err != nil {
		return err
	}
	switch {
	case !at.After(p.importedTo):
		im.rep.AlreadyImported++
		return nil
	case p.liveFrom != nil && !at.Before(*p.liveFrom):
		im.rep.AfterLive++
		return nil
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
	// Recorded last: a run cut off between the writes above and here
	// redoes this one row, whose snapshot then dedupes onto itself.
	if err := im.s.Tx(ctx, func(tx *sqlx.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO legacy_import_progress (binding_id, org_id, imported_to) VALUES ($1, $2, $3)
			ON CONFLICT (binding_id) DO UPDATE SET imported_to = EXCLUDED.imported_to, updated_at = now()`,
			bindingID, im.s.OrgID(), at)
		return err
	}); err != nil {
		return err
	}
	p.importedTo = at
	im.rep.Imported++
	return nil
}

// enterMonth makes sure at's month has its partitions, and rolls up the
// month before once the import has moved past it.
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
	im.month = month
	return nil
}

// finishMonth rolls the whole calendar month in hand up into the daily
// tier, and into the hourly tier for the part the live server keeps
// hourly rollups for (the last 45 days) — older hourly buckets would be
// deleted by its next pass anyway.
func (im *importer) finishMonth(ctx context.Context) error {
	if im.month.IsZero() {
		return nil
	}
	from, to := im.month, im.month.AddDate(0, 1, 0)
	if now := time.Now(); to.After(now) {
		to = now.Truncate(time.Hour)
	}
	hourlyFrom := from
	if keep := time.Now().Add(-timeseries.DefaultHourlyRetention).Truncate(time.Hour); keep.After(hourlyFrom) {
		hourlyFrom = keep
	}
	if hourlyFrom.Before(to) {
		if err := timeseries.BackfillHourly(ctx, im.ddl, hourlyFrom, to); err != nil {
			return err
		}
	}
	if err := timeseries.BackfillDaily(ctx, im.ddl, from, to); err != nil {
		return err
	}
	if im.opts.Progress != nil {
		im.opts.Progress(im.month, im.rep.Imported)
	}
	im.month = time.Time{}
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
