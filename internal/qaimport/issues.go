package qaimport

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"mime"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/repo"
)

func (im *Importer) importIssues(ctx context.Context, w *repo.QAImportWriter, legacyProject uuid.UUID, st *projectState, rep *Report) error {
	issues, err := im.legacy.Issues(ctx, legacyProject)
	if err != nil {
		return err
	}
	codes := make([]string, len(issues))
	for i, is := range issues {
		codes[i] = is.Code
	}
	keys := AllocateKeys(codes)
	for i, is := range issues {
		title := TruncateRunes(strings.TrimSpace(is.Title), repo.MaxQATitleLength)
		if title == "" {
			title = "Untitled issue"
		}
		var planID, sectionID *uuid.UUID
		if is.ReleaseID != nil {
			if p, ok := st.plans[*is.ReleaseID]; ok {
				planID = &p
			}
		}
		if is.ModuleID != nil {
			if s, ok := st.sections[*is.ModuleID]; ok {
				sectionID = &s
			}
		}
		renumbered := is.Code != "" && keys[i] != keyOf(is.Code)
		description := CleanText(is.Description)
		if renumbered {
			description = WithLegacyCode(description, is.Code)
		}
		id, err := w.InsertIssue(repo.QAIssue{
			ProjectID: st.projectID, Key: keys[i], Title: title, Description: description,
			StepsToReproduce: CleanText(is.Steps), Severity: MapSeverity(is.Severity, is.Priority),
			Status: MapIssueStatus(is.Status), Environment: CleanText(is.Environment), PlanID: planID,
			SectionID: sectionID, AssigneeID: im.user(is.AssigneeID), ReportedBy: im.user(is.CreatedBy),
			Source: "internal", CreatedAt: is.CreatedAt, UpdatedAt: is.UpdatedAt,
		})
		if err != nil {
			return fmt.Errorf("issue %s %q: %w", is.Code, title, err)
		}
		st.issues[is.ID] = id
		if renumbered {
			rep.warn("issue %s %q renumbered to BUG-%d", is.Code, title, keys[i])
		}
		if err := w.Remember("test_issues", is.ID, id); err != nil {
			return err
		}
		rep.imported("issues", 1)
	}

	links, err := im.legacy.IssueLinks(ctx, legacyProject)
	if err != nil {
		return err
	}
	for _, l := range links {
		itemID, ok1 := st.items[l.DetailID]
		issueID, ok2 := st.issues[l.IssueID]
		if !ok1 || !ok2 {
			rep.skipped("issue_links", "item or issue not imported")
			continue
		}
		if err := w.InsertIssueLink(itemID, issueID, l.CreatedAt); err != nil {
			return err
		}
		rep.imported("issue_links", 1)
	}

	type event struct {
		at     time.Time
		legacy uuid.UUID
		isNote bool
		ev     repo.QAIssueEvent
	}
	var events []event
	comments, err := im.legacy.Comments(ctx, legacyProject)
	if err != nil {
		return err
	}
	for _, c := range comments {
		issueID, ok := st.issues[c.IssueID]
		if !ok {
			continue
		}
		events = append(events, event{at: c.CreatedAt, legacy: c.ID, isNote: true, ev: repo.QAIssueEvent{
			IssueID: issueID, Kind: "comment", Body: strings.TrimSpace(c.Body), ActorID: im.user(c.CreatedBy), CreatedAt: c.CreatedAt,
		}})
	}
	logs, err := im.legacy.IssueLogs(ctx, legacyProject)
	if err != nil {
		return err
	}
	for _, l := range logs {
		issueID, ok := st.issues[l.IssueID]
		if !ok {
			continue
		}
		from, to := MapIssueStatus(l.From), MapIssueStatus(l.To)
		if from == to {
			rep.skipped("issue_events", "no change")
			continue
		}
		events = append(events, event{at: l.CreatedAt, legacy: l.ID, ev: repo.QAIssueEvent{
			IssueID: issueID, Kind: "status", StatusFrom: &from, StatusTo: &to, ActorID: im.user(l.By), CreatedAt: l.CreatedAt,
		}})
	}
	sort.SliceStable(events, func(i, j int) bool { return events[i].at.Before(events[j].at) })
	for _, e := range events {
		id, err := w.InsertIssueEvent(e.ev)
		if err != nil {
			return err
		}
		table := "test_issue_logs"
		if e.isNote {
			table = "comments"
			st.comments[e.legacy] = id
		}
		if err := w.Remember(table, e.legacy, id); err != nil {
			return err
		}
		rep.imported("issue_events", 1)
	}

	return im.importFiles(ctx, w, legacyProject, st, rep)
}

func (im *Importer) importFiles(ctx context.Context, w *repo.QAImportWriter, legacyProject uuid.UUID, st *projectState, rep *Report) error {
	files, err := im.legacy.Files(ctx, legacyProject)
	if err != nil {
		return err
	}
	root, _ := filepath.Abs(im.opt.FilesDir)
	for _, f := range files {
		if im.opt.FilesDir == "" {
			rep.skipped("attachments", "files not requested")
			continue
		}
		var owner repo.QAAttachmentOwner
		switch f.OwnerModel {
		case `App\Models\Result`:
			if id, ok := st.results[f.OwnerID]; ok {
				owner.ResultID = &id
			}
		case `App\Models\TestIssue`:
			if id, ok := st.issues[f.OwnerID]; ok {
				owner.IssueID = &id
			}
		case `App\Models\Comment`:
			if id, ok := st.comments[f.OwnerID]; ok {
				owner.EventID = &id
			}
		}
		if owner.ResultID == nil && owner.IssueID == nil && owner.EventID == nil {
			rep.skipped("attachments", "owner not imported")
			continue
		}
		src := filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(f.Path, "/")))
		if rel, err := filepath.Rel(root, src); err != nil || strings.HasPrefix(rel, "..") {
			rep.skipped("attachments", "path outside files dir")
			continue
		}
		file, err := os.Open(src)
		if errors.Is(err, fs.ErrNotExist) {
			rep.skipped("attachments", "file missing")
			rep.warn("attachment %s missing at %s", f.ID, f.Path)
			continue
		}
		if err != nil {
			return err
		}
		key := fmt.Sprintf("qa/%s/legacy/%s", im.opt.OrgID, f.ID)
		var size int64
		if im.opt.DryRun {
			// A dry run writes nothing, the blob store included.
			info, err := file.Stat()
			file.Close()
			if err != nil {
				return err
			}
			size = info.Size()
		} else {
			size, err = im.store.Put(ctx, key, file)
			file.Close()
			if err != nil {
				return fmt.Errorf("copy %s: %w", f.Path, err)
			}
		}
		name := f.Name
		if name == "" {
			name = filepath.Base(f.Path)
		}
		ctype := mime.TypeByExtension(strings.ToLower(filepath.Ext(name)))
		if ctype == "" {
			ctype = "application/octet-stream"
		}
		id, err := w.InsertAttachment(repo.QAAttachment{
			ResultID: owner.ResultID, IssueID: owner.IssueID, EventID: owner.EventID,
			FileName: TruncateRunes(name, 255), ContentType: ctype, SizeBytes: size, StorageKey: key, CreatedAt: f.CreatedAt,
		})
		if err != nil {
			return err
		}
		if err := w.Remember("minio", f.ID, id); err != nil {
			return err
		}
		rep.imported("attachments", 1)
	}
	return nil
}
