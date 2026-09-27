package repo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/blob"
	"github.com/uyab/lotus/internal/tenant"
)

const MaxQAAttachmentBytes = 20 << 20

var ErrQAAttachmentTooLarge = errors.New("attachment too large")

type QAAttachmentOwner struct {
	ResultID, IssueID, EventID *uuid.UUID
}

func (o QAAttachmentOwner) valid() bool {
	n := 0
	for _, p := range []*uuid.UUID{o.ResultID, o.IssueID, o.EventID} {
		if p != nil {
			n++
		}
	}
	return n == 1
}

type QAAttachment struct {
	ID          uuid.UUID  `db:"id"`
	OrgID       uuid.UUID  `db:"org_id"`
	ResultID    *uuid.UUID `db:"result_id"`
	IssueID     *uuid.UUID `db:"issue_id"`
	EventID     *uuid.UUID `db:"event_id"`
	FileName    string     `db:"file_name"`
	ContentType string     `db:"content_type"`
	SizeBytes   int64      `db:"size_bytes"`
	StorageKey  string     `db:"storage_key"`
	CreatedBy   *uuid.UUID `db:"created_by"`
	CreatedAt   time.Time  `db:"created_at"`
}

const qaAttachmentCols = `id, org_id, result_id, issue_id, event_id, file_name, content_type, size_bytes, storage_key, created_by, created_at`

// cleanFileName keeps only the base name, trimmed and at most 255 characters.
func cleanFileName(name string) string {
	name = strings.TrimSpace(path.Base(strings.ReplaceAll(name, `\`, "/")))
	if name == "" || name == "." || name == "/" {
		name = "file"
	}
	for utf8.RuneCountInString(name) > 255 {
		_, size := utf8.DecodeLastRuneInString(name)
		name = name[:len(name)-size]
	}
	return name
}

func SaveQAAttachment(ctx context.Context, s tenant.Scope, store blob.Store, owner QAAttachmentOwner, fileName, contentType string, r io.Reader, createdBy *uuid.UUID) (*QAAttachment, error) {
	if !owner.valid() {
		return nil, ErrInvalidInput
	}
	// Read one byte past the limit to detect oversize input without
	// trusting a client-supplied length.
	head := make([]byte, 512)
	n, err := io.ReadFull(r, head)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return nil, err
	}
	head = head[:n]
	if contentType == "" {
		contentType = http.DetectContentType(head)
	}
	now := time.Now().UTC()
	id := uuid.New()
	key := fmt.Sprintf("qa/%s/%04d/%02d/%s", s.OrgID(), now.Year(), now.Month(), id)
	limited := io.LimitReader(io.MultiReader(bytes.NewReader(head), r), MaxQAAttachmentBytes+1)
	size, err := store.Put(ctx, key, limited)
	if err != nil {
		return nil, err
	}
	if size > MaxQAAttachmentBytes {
		store.Delete(ctx, key) //nolint:errcheck // best effort cleanup
		return nil, ErrQAAttachmentTooLarge
	}
	var out QAAttachment
	err = s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.GetContext(ctx, &out, `
			INSERT INTO qa_attachments (id, org_id, result_id, issue_id, event_id, file_name, content_type, size_bytes, storage_key, created_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10) RETURNING `+qaAttachmentCols,
			id, s.OrgID(), owner.ResultID, owner.IssueID, owner.EventID, cleanFileName(fileName), contentType, size, key, createdBy)
	})
	if err != nil {
		store.Delete(ctx, key) //nolint:errcheck // the row is the source of truth; drop the orphan
		return nil, qaWriteErr(err)
	}
	return &out, nil
}

func GetQAAttachment(ctx context.Context, s tenant.Scope, id uuid.UUID) (*QAAttachment, error) {
	var out QAAttachment
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.GetContext(ctx, &out, `SELECT `+qaAttachmentCols+` FROM qa_attachments WHERE id = $1`, id)
	})
	if err != nil {
		return nil, qaWriteErr(err)
	}
	return &out, nil
}

func ListQAAttachments(ctx context.Context, s tenant.Scope, owner QAAttachmentOwner) ([]QAAttachment, error) {
	if !owner.valid() {
		return nil, ErrInvalidInput
	}
	var out []QAAttachment
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &out, `
			SELECT `+qaAttachmentCols+` FROM qa_attachments
			WHERE result_id IS NOT DISTINCT FROM $1 AND issue_id IS NOT DISTINCT FROM $2 AND event_id IS NOT DISTINCT FROM $3
			ORDER BY created_at, id`, owner.ResultID, owner.IssueID, owner.EventID)
	})
	return out, err
}

func DeleteQAAttachment(ctx context.Context, s tenant.Scope, store blob.Store, id uuid.UUID) error {
	var key string
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.GetContext(ctx, &key, `DELETE FROM qa_attachments WHERE id = $1 RETURNING storage_key`, id)
	})
	if err != nil {
		return qaWriteErr(err)
	}
	return store.Delete(ctx, key)
}

func QAAttachmentProjectID(ctx context.Context, s tenant.Scope, id uuid.UUID) (uuid.UUID, error) {
	var pid uuid.UUID
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.GetContext(ctx, &pid, `
			SELECT COALESCE(pl.project_id, iss.project_id, eiss.project_id)
			FROM qa_attachments a
			LEFT JOIN qa_results r ON r.id = a.result_id
			LEFT JOIN qa_run_items i ON i.id = r.item_id
			LEFT JOIN qa_runs ru ON ru.id = i.run_id
			LEFT JOIN qa_plans pl ON pl.id = ru.plan_id
			LEFT JOIN qa_issues iss ON iss.id = a.issue_id
			LEFT JOIN qa_issue_events e ON e.id = a.event_id
			LEFT JOIN qa_issues eiss ON eiss.id = e.issue_id
			WHERE a.id = $1`, id)
	})
	return pid, qaWriteErr(err)
}
