package repo

import (
	"context"
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
)

const (
	MaxQANameLength   = 200
	MaxQATitleLength  = 500
	MaxQASectionDepth = 8
)

// QAPhases is the fixed list of testing phases a scenario or plan belongs to.
var QAPhases = []string{"smoke", "sit", "uat", "regression", "other"}

var (
	ErrQANameTaken      = errors.New("that name is already used here")
	ErrQASectionCycle   = errors.New("a section cannot be moved into itself or its own subsection")
	ErrQASectionTooDeep = errors.New("sections cannot nest that deep")
	ErrQAClosed         = errors.New("closed")
	ErrQAEmptyRun       = errors.New("the run would have no cases")
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

func qaWriteErr(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505":
			return ErrQANameTaken
		case "23503":
			return ErrNotFound
		case "23514", "22P02":
			return ErrInvalidInput
		}
	}
	if isNoRows(err) {
		return ErrNotFound
	}
	return err
}
