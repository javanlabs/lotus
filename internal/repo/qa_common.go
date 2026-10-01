package repo

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/tenant"
)

const (
	MaxQANameLength   = 200
	MaxQATitleLength  = 500
	MaxQASectionDepth = 8
	// MaxQACommentLength bounds a result's comment, in characters, however
	// it is recorded (page, API or MCP).
	MaxQACommentLength = 2500
	// MaxQARunNotesLength bounds a run's notes as written in Lotus. The
	// importer is not held to it: legacy descriptions may run longer.
	MaxQARunNotesLength = 2000
)

// QAPhases is the fixed list of testing phases a scenario or plan belongs to.
var QAPhases = []string{"smoke", "sit", "uat", "regression", "other"}

var (
	ErrQANameTaken      = errors.New("that name is already used here")
	ErrQASectionCycle   = errors.New("a section cannot be moved into itself or its own subsection")
	ErrQASectionTooDeep = errors.New("sections cannot nest that deep")
	// ErrQASectionArchived: nothing new goes into an archived section —
	// no case, no subsection, no restore of a case still filed there.
	ErrQASectionArchived = errors.New("that section is archived")
	ErrQAClosed          = errors.New("closed")
	ErrQAEmptyRun        = errors.New("the run would have no cases")
	// ErrQACommentTooLong and ErrQANotesTooLong are ErrInvalidInput, named
	// so a caller can say which limit was passed.
	ErrQACommentTooLong = fmt.Errorf("%w: comment too long", ErrInvalidInput)
	ErrQANotesTooLong   = fmt.Errorf("%w: notes too long", ErrInvalidInput)
)

// QAStep is one step of a test case.
type QAStep struct {
	Action   string `json:"action"`
	Expected string `json:"expected"`
}

// QASteps is stored as a jsonb array. It is never written as JSON null and
// never read back as a nil slice, so templates can range over it freely.
type QASteps []QAStep

func (s QASteps) Value() (driver.Value, error) {
	if s == nil {
		return []byte("[]"), nil
	}
	return json.Marshal([]QAStep(s))
}

func (s *QASteps) Scan(src any) error {
	var b []byte
	switch v := src.(type) {
	case nil:
		*s = QASteps{}
		return nil
	case []byte:
		b = v
	case string:
		b = []byte(v)
	default:
		return fmt.Errorf("QASteps: cannot scan %T", src)
	}
	var out []QAStep
	if err := json.Unmarshal(b, &out); err != nil {
		return err
	}
	if out == nil {
		out = []QAStep{}
	}
	*s = out
	return nil
}

func normalizeQAName(s string, max int) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" || utf8.RuneCountInString(s) > max {
		return "", ErrInvalidInput
	}
	return s, nil
}

func validQAPhase(p string) bool { return slices.Contains(QAPhases, p) }

// nextQAKey allocates the next per-project key for qa_cases or qa_issues.
// The advisory lock serialises allocation per project for the rest of the
// transaction, so two concurrent creates never collide on UNIQUE (project_id, key).
func nextQAKey(ctx context.Context, tx *sqlx.Tx, table string, projectID uuid.UUID) (int, error) {
	if table != "qa_cases" && table != "qa_issues" {
		return 0, fmt.Errorf("nextQAKey: unsupported table %q", table)
	}
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`,
		table+":"+projectID.String()); err != nil {
		return 0, err
	}
	var next int
	err := tx.GetContext(ctx, &next,
		`SELECT COALESCE(max(key), 0) + 1 FROM `+table+` WHERE project_id = $1`, projectID)
	return next, err
}

// sectionInProject fails with ErrNotFound unless sectionID belongs to projectID.
func sectionInProject(ctx context.Context, tx *sqlx.Tx, sectionID, projectID uuid.UUID) error {
	var ok bool
	if err := tx.GetContext(ctx, &ok,
		`SELECT EXISTS (SELECT 1 FROM qa_sections WHERE id = $1 AND project_id = $2)`, sectionID, projectID); err != nil {
		return err
	}
	if !ok {
		return ErrNotFound
	}
	return nil
}

// liveSectionInProject is sectionInProject for a section something is being
// put into: it must also not be archived.
func liveSectionInProject(ctx context.Context, tx *sqlx.Tx, sectionID, projectID uuid.UUID) error {
	var archived *bool
	if err := tx.GetContext(ctx, &archived,
		`SELECT archived_at IS NOT NULL FROM qa_sections WHERE id = $1 AND project_id = $2`, sectionID, projectID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if *archived {
		return ErrQASectionArchived
	}
	return nil
}

func planInProject(ctx context.Context, tx *sqlx.Tx, planID, projectID uuid.UUID) error {
	var ok bool
	if err := tx.GetContext(ctx, &ok,
		`SELECT EXISTS (SELECT 1 FROM qa_plans WHERE id = $1 AND project_id = $2)`, planID, projectID); err != nil {
		return err
	}
	if !ok {
		return ErrNotFound
	}
	return nil
}

func qaWriteErr(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505":
			return ErrQANameTaken
		case "23503":
			return ErrNotFound
		case "23514", "22P02", "22021", "22P05":
			// 22021/22P05: a NUL character, which text and jsonb cannot hold.
			return ErrInvalidInput
		}
	}
	if isNoRows(err) {
		return ErrNotFound
	}
	return err
}

// QAKind names a QA row type for QAProjectOf.
type QAKind string

const (
	QAKindSection       QAKind = "section"
	QAKindCase          QAKind = "case"
	QAKindScenario      QAKind = "scenario"
	QAKindScenarioGroup QAKind = "scenario_group"
	QAKindPlan          QAKind = "plan"
	QAKindRun           QAKind = "run"
	QAKindItem          QAKind = "item"
	QAKindResult        QAKind = "result"
	QAKindIssue         QAKind = "issue"
	QAKindEvent         QAKind = "issue_event"
)

// qaProjectOfQueries resolve one row's project. Each takes the row id as $1.
var qaProjectOfQueries = map[QAKind]string{
	QAKindSection:       `SELECT project_id FROM qa_sections WHERE id = $1`,
	QAKindCase:          `SELECT project_id FROM qa_cases WHERE id = $1`,
	QAKindScenario:      `SELECT project_id FROM qa_scenarios WHERE id = $1`,
	QAKindScenarioGroup: `SELECT sc.project_id FROM qa_scenario_groups g JOIN qa_scenarios sc ON sc.id = g.scenario_id WHERE g.id = $1`,
	QAKindPlan:          `SELECT project_id FROM qa_plans WHERE id = $1`,
	QAKindRun:           `SELECT p.project_id FROM qa_runs r JOIN qa_plans p ON p.id = r.plan_id WHERE r.id = $1`,
	QAKindItem: `SELECT p.project_id FROM qa_run_items i JOIN qa_runs r ON r.id = i.run_id
		JOIN qa_plans p ON p.id = r.plan_id WHERE i.id = $1`,
	QAKindResult: `SELECT p.project_id FROM qa_results res JOIN qa_run_items i ON i.id = res.item_id
		JOIN qa_runs r ON r.id = i.run_id JOIN qa_plans p ON p.id = r.plan_id WHERE res.id = $1`,
	QAKindIssue: `SELECT project_id FROM qa_issues WHERE id = $1`,
	QAKindEvent: `SELECT iss.project_id FROM qa_issue_events e JOIN qa_issues iss ON iss.id = e.issue_id WHERE e.id = $1`,
}

// QAProjectOf returns the project a QA row belongs to. RLS confines every
// query to the organization, not to a project, and the by-id functions in
// this package look rows up by id alone; a handler serving
// /projects/:id/qa/... must compare this against :id (and answer 404 on a
// mismatch) for every id it takes from the URL or the form.
// Attachments have their own resolver, QAAttachmentProjectID.
func QAProjectOf(ctx context.Context, s tenant.Scope, kind QAKind, id uuid.UUID) (uuid.UUID, error) {
	q, ok := qaProjectOfQueries[kind]
	if !ok {
		return uuid.Nil, ErrInvalidInput
	}
	var projectID uuid.UUID
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.GetContext(ctx, &projectID, q, id)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return uuid.Nil, ErrNotFound
	}
	return projectID, err
}

// truncateQAName cuts a derived name (a run's "Scenario / Group" label
// turned into a group name) to MaxQANameLength, ending it with "…".
func truncateQAName(s string) string {
	r := []rune(s)
	if len(r) <= MaxQANameLength {
		return s
	}
	return string(r[:MaxQANameLength-1]) + "…"
}
