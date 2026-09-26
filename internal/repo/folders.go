package repo

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/tenant"
)

// MaxFolderDepth is how many levels folders may nest; a top-level folder
// is level 1. Deep enough for division → tribe lead → squad and more, and
// shallow enough that the indented tree stays readable.
const MaxFolderDepth = 8

// MaxFolderNameLength bounds a folder name, in characters. The database
// CHECK enforces the same number.
const MaxFolderNameLength = 80

var (
	// ErrFolderNameTaken: a sibling folder already has this name, ignoring
	// case. Top-level folders are one sibling group.
	ErrFolderNameTaken = errors.New("a folder with that name already exists here")
	// ErrFolderCycle: the move would put a folder inside itself.
	ErrFolderCycle = errors.New("a folder cannot be moved into itself or its own subfolder")
	// ErrFolderTooDeep: the create or move would nest past MaxFolderDepth.
	ErrFolderTooDeep = errors.New("folders cannot nest that deep")
)

// FolderNotEmptyError refuses deleting a folder that still holds anything.
// Archived projects count: they keep their folder so unarchiving can put
// them back, so a folder that looks empty can still hold them — which is
// why the message names every project, archived or not.
type FolderNotEmptyError struct {
	Name       string
	Subfolders int
	Projects   []string // every project filed here, archived included, by name
	Archived   int      // how many of Projects are archived
}

// maxNamedInError caps how many project names the message lists.
const maxNamedInError = 5

func (e *FolderNotEmptyError) Error() string {
	var parts []string
	if e.Subfolders > 0 {
		parts = append(parts, fmt.Sprintf("%d folder%s", e.Subfolders, pluralS(e.Subfolders)))
	}
	if n := len(e.Projects); n > 0 {
		names, more := e.Projects, ""
		if n > maxNamedInError {
			names, more = names[:maxNamedInError], fmt.Sprintf(" +%d more", n-maxNamedInError)
		}
		p := fmt.Sprintf("%d project%s (%s%s", n, pluralS(n), strings.Join(names, ", "), more)
		if e.Archived > 0 {
			p += fmt.Sprintf(" — %d archived", e.Archived)
		}
		parts = append(parts, p+")")
	}
	if len(parts) == 0 {
		return fmt.Sprintf("'%s' is not empty.", e.Name)
	}
	return fmt.Sprintf("'%s' still contains %s.", e.Name, strings.Join(parts, " and "))
}

func pluralS(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// Folder is one node of an organization's project tree.
type Folder struct {
	ID        uuid.UUID  `db:"id"`
	OrgID     uuid.UUID  `db:"org_id"`
	ParentID  *uuid.UUID `db:"parent_id"` // nil at the top level
	Name      string     `db:"name"`
	SortOrder int        `db:"sort_order"`
	CreatedAt time.Time  `db:"created_at"`
}

const folderCols = `id, org_id, parent_id, name, sort_order, created_at`

func normalizeFolderName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > MaxFolderNameLength {
		return "", ErrInvalidInput
	}
	return name, nil
}

// folderWriteErr maps the constraint violations a folder write can hit to
// the package's sentinels.
func folderWriteErr(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505":
			return ErrFolderNameTaken
		case "23503":
			// A parent or folder this organization cannot see: RLS hides
			// the row, the composite foreign key refuses the reference.
			return ErrNotFound
		case "23514":
			return ErrInvalidInput
		}
	}
	if isNoRows(err) {
		return ErrNotFound
	}
	return err
}

// lockFolderTree serializes every change to the shape of one
// organization's tree for the rest of the transaction. The cycle and depth
// checks read the whole tree and then write; without the lock two moves
// that are each valid alone could together form a loop.
func lockFolderTree(ctx context.Context, tx *sqlx.Tx, orgID uuid.UUID) error {
	_, err := tx.ExecContext(ctx,
		`SELECT pg_advisory_xact_lock(hashtextextended('lotus.folders:' || $1::text, 0))`, orgID.String())
	return err
}

// loadFolderParents reads the organization's whole tree as child → parent.
// An organization has tens of folders, so reading them all is cheaper and
// simpler than a recursive query per check.
func loadFolderParents(ctx context.Context, tx *sqlx.Tx) (map[uuid.UUID]*uuid.UUID, error) {
	var rows []struct {
		ID       uuid.UUID  `db:"id"`
		ParentID *uuid.UUID `db:"parent_id"`
	}
	if err := tx.SelectContext(ctx, &rows, `SELECT id, parent_id FROM folders`); err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID]*uuid.UUID, len(rows))
	for _, r := range rows {
		out[r.ID] = r.ParentID
	}
	return out, nil
}

// folderDepth is id's level: 1 at the top. The step bound means even a
// corrupt loop in the data cannot hang the walk.
func folderDepth(parents map[uuid.UUID]*uuid.UUID, id uuid.UUID) int {
	depth := 0
	for cur := &id; cur != nil && depth <= len(parents); cur = parents[*cur] {
		depth++
	}
	return depth
}

func CreateFolder(ctx context.Context, s tenant.Scope, name string, parentID *uuid.UUID) (*Folder, error) {
	name, err := normalizeFolderName(name)
	if err != nil {
		return nil, err
	}
	var out Folder
	err = s.Tx(ctx, func(tx *sqlx.Tx) error {
		if err := lockFolderTree(ctx, tx, s.OrgID()); err != nil {
			return err
		}
		if parentID != nil {
			parents, err := loadFolderParents(ctx, tx)
			if err != nil {
				return err
			}
			if _, ok := parents[*parentID]; !ok {
				return ErrNotFound
			}
			if folderDepth(parents, *parentID)+1 > MaxFolderDepth {
				return ErrFolderTooDeep
			}
		}
		return tx.GetContext(ctx, &out, `
			INSERT INTO folders (org_id, parent_id, name) VALUES ($1, $2, $3)
			RETURNING `+folderCols, s.OrgID(), parentID, name)
	})
	if err != nil {
		return nil, folderWriteErr(err)
	}
	return &out, nil
}

// ListFolders returns every folder in the organization. The caller builds
// the tree; the order here is only the order siblings are shown in.
func ListFolders(ctx context.Context, s tenant.Scope) ([]Folder, error) {
	var out []Folder
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &out,
			`SELECT `+folderCols+` FROM folders ORDER BY sort_order, lower(name)`)
	})
	return out, err
}

func FolderByID(ctx context.Context, s tenant.Scope, id uuid.UUID) (*Folder, error) {
	var out Folder
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.GetContext(ctx, &out, `SELECT `+folderCols+` FROM folders WHERE id = $1`, id)
	})
	if err != nil {
		return nil, folderWriteErr(err)
	}
	return &out, nil
}

func RenameFolder(ctx context.Context, s tenant.Scope, id uuid.UUID, name string) (*Folder, error) {
	name, err := normalizeFolderName(name)
	if err != nil {
		return nil, err
	}
	var out Folder
	err = s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.GetContext(ctx, &out,
			`UPDATE folders SET name = $2 WHERE id = $1 RETURNING `+folderCols, id, name)
	})
	if err != nil {
		return nil, folderWriteErr(err)
	}
	return &out, nil
}

// MoveProject files a project in folderID, or at the root when folderID is
// nil, and returns the folder it was in before (nil for the root).
func MoveProject(ctx context.Context, s tenant.Scope, projectID uuid.UUID, folderID *uuid.UUID) (*uuid.UUID, error) {
	var from *uuid.UUID
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		if err := tx.GetContext(ctx, &from,
			`SELECT folder_id FROM projects WHERE id = $1 FOR UPDATE`, projectID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE projects SET folder_id = $2 WHERE id = $1`, projectID, folderID)
		return err
	})
	if err != nil {
		return nil, folderWriteErr(err)
	}
	return from, nil
}
