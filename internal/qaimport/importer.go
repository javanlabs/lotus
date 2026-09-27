package qaimport

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/blob"
	"github.com/uyab/lotus/internal/repo"
	"github.com/uyab/lotus/internal/repo/global"
	"github.com/uyab/lotus/internal/repo/legacyqa"
	"github.com/uyab/lotus/internal/tenant"
)

type Options struct {
	OrgID           uuid.UUID
	UsersMap        map[string]string    // legacy email (lower) -> canonical email (lower)
	ProjectsMap     map[uuid.UUID]string // legacy project id -> existing Lotus project slug
	FolderName      string               // folder for newly created projects; "" = root
	DormantBefore   time.Time            // no activity since then -> user row, no membership
	CloseIdleBefore time.Time            // plans whose last activity is older get closed; zero = close none
	FilesDir        string               // mirror of the legacy bucket; "" = skip attachments
	DryRun          bool
}

type Importer struct {
	db     *sqlx.DB
	scope  tenant.Scope
	legacy *legacyqa.Reader
	store  blob.Store
	opt    Options
	rep    *Report
	users  map[uuid.UUID]uuid.UUID // legacy user id -> Lotus user id
	guests map[uuid.UUID]bool      // legacy user ids whose person is a guest
}

var errDryRun = errors.New("dry run: roll back")

func New(db *sqlx.DB, legacy *legacyqa.Reader, store blob.Store, opt Options) *Importer {
	return &Importer{
		db: db, scope: tenant.FromRun(db, opt.OrgID), legacy: legacy, store: store, opt: opt,
		rep: newReport(), users: map[uuid.UUID]uuid.UUID{}, guests: map[uuid.UUID]bool{},
	}
}

func (im *Importer) Run(ctx context.Context) (*Report, error) {
	if err := im.importUsers(ctx); err != nil {
		return im.rep, fmt.Errorf("users: %w", err)
	}
	projects, err := im.legacy.Projects(ctx)
	if err != nil {
		return im.rep, err
	}
	folderID, err := im.ensureFolder(ctx)
	if err != nil {
		return im.rep, fmt.Errorf("folder: %w", err)
	}
	for _, lp := range projects {
		if !lp.HasData {
			im.rep.ProjectsSkipped++
			im.rep.skipped("projects", "no QA data")
			continue
		}
		if err := im.importProject(ctx, lp, folderID); err != nil {
			return im.rep, fmt.Errorf("project %q (%s): %w", lp.Title, lp.ID, err)
		}
	}
	return im.rep, nil
}

// user maps a legacy user id to a Lotus user id (nil when unknown or dry run).
func (im *Importer) user(id *uuid.UUID) *uuid.UUID {
	if id == nil {
		return nil
	}
	u, ok := im.users[*id]
	if !ok || u == uuid.Nil {
		return nil
	}
	return &u
}

func (im *Importer) importUsers(ctx context.Context) error {
	legacyUsers, err := im.legacy.Users(ctx)
	if err != nil {
		return err
	}
	last, err := im.legacy.LastActivity(ctx)
	if err != nil {
		return err
	}
	type person struct {
		name     string
		role     string
		active   bool
		lastSeen time.Time
		ids      []uuid.UUID
	}
	people := map[string]*person{}
	for _, u := range legacyUsers {
		email := strings.ToLower(strings.TrimSpace(u.Email))
		if c, ok := im.opt.UsersMap[email]; ok {
			email = strings.ToLower(c)
		}
		p := people[email]
		if p == nil {
			p = &person{name: strings.TrimSpace(u.FirstName + " " + u.LastName)}
			people[email] = p
		}
		p.ids = append(p.ids, u.ID)
		if r := MapRole(u.UserType); RoleRank(r) > RoleRank(p.role) {
			p.role = r
		}
		p.active = p.active || u.Active
		if t := last[u.ID]; t.After(p.lastSeen) {
			p.lastSeen = t
		}
	}
	emails := make([]string, 0, len(people))
	for e := range people {
		emails = append(emails, e)
	}
	sort.Strings(emails)
	for _, email := range emails {
		p := people[email]
		if p.role == "guest" {
			for _, id := range p.ids {
				im.guests[id] = true
			}
		}
		u, err := global.UserByEmail(ctx, im.db, email)
		if err != nil {
			return err
		}
		if im.opt.DryRun {
			// Reads only: report what a real run would create.
			if u == nil {
				im.rep.imported("users", 1)
			} else {
				im.rep.skipped("users", "already exists")
			}
			if p.lastSeen.IsZero() || p.lastSeen.Before(im.opt.DormantBefore) {
				im.rep.skipped("memberships", "dormant")
			} else if u == nil {
				im.rep.imported("memberships", 1)
			} else if m, _ := global.ActiveMembership(ctx, im.db, u.ID, im.opt.OrgID); m != nil {
				im.rep.skipped("memberships", "already a member")
			} else {
				im.rep.imported("memberships", 1)
			}
			continue
		}
		if u == nil {
			e := email
			u, err = global.CreateUser(ctx, im.db, global.NewUser{Email: &e, EmailVerified: true, Name: p.name})
			if err != nil {
				return fmt.Errorf("create %s: %w", email, err)
			}
			im.rep.imported("users", 1)
		} else {
			im.rep.skipped("users", "already exists")
		}
		for _, id := range p.ids {
			im.users[id] = u.ID
		}
		if p.lastSeen.IsZero() || p.lastSeen.Before(im.opt.DormantBefore) {
			im.rep.skipped("memberships", "dormant")
			continue
		}
		status := "active"
		if !p.active {
			status = "suspended"
		}
		created, err := global.EnsureMembership(ctx, im.db, im.opt.OrgID, u.ID, p.role, status)
		if err != nil {
			return err
		}
		if created {
			im.rep.imported("memberships", 1)
		} else {
			im.rep.skipped("memberships", "already a member")
		}
	}
	return nil
}

func (im *Importer) ensureFolder(ctx context.Context) (*uuid.UUID, error) {
	if im.opt.FolderName == "" || im.opt.DryRun {
		return nil, nil
	}
	folders, err := repo.ListFolders(ctx, im.scope)
	if err != nil {
		return nil, err
	}
	for _, f := range folders {
		if f.ParentID == nil && strings.EqualFold(f.Name, im.opt.FolderName) {
			return &f.ID, nil
		}
	}
	f, err := repo.CreateFolder(ctx, im.scope, im.opt.FolderName, nil)
	if err != nil {
		return nil, err
	}
	return &f.ID, nil
}

// projectState carries legacy -> new id maps for one project's transaction.
type projectState struct {
	projectID      uuid.UUID
	sections       map[uuid.UUID]uuid.UUID  // legacy module -> section
	sectionParents map[uuid.UUID]*uuid.UUID // section -> parent section (nil at top level)
	paths          map[uuid.UUID]string     // section -> "Auth / Login"
	unsorted       *uuid.UUID
	cases          map[uuid.UUID]importedCase
	plans          map[uuid.UUID]uuid.UUID // legacy test plan or release -> plan
	unplanned      *uuid.UUID
	lastSeen       map[uuid.UUID]time.Time // plan -> last activity
	runs           map[uuid.UUID]uuid.UUID // legacy run -> run
	runPlans       map[uuid.UUID]uuid.UUID // run -> plan
	items          map[uuid.UUID]uuid.UUID // legacy detail -> item
	itemRuns       map[uuid.UUID]uuid.UUID // item -> run
	results        map[uuid.UUID]uuid.UUID
	issues         map[uuid.UUID]uuid.UUID
	comments       map[uuid.UUID]uuid.UUID // legacy comment -> event
}

func newProjectState(projectID uuid.UUID) *projectState {
	return &projectState{
		projectID: projectID, sections: map[uuid.UUID]uuid.UUID{}, sectionParents: map[uuid.UUID]*uuid.UUID{},
		paths: map[uuid.UUID]string{}, cases: map[uuid.UUID]importedCase{}, plans: map[uuid.UUID]uuid.UUID{},
		lastSeen: map[uuid.UUID]time.Time{}, runs: map[uuid.UUID]uuid.UUID{}, runPlans: map[uuid.UUID]uuid.UUID{},
		items: map[uuid.UUID]uuid.UUID{}, itemRuns: map[uuid.UUID]uuid.UUID{}, results: map[uuid.UUID]uuid.UUID{},
		issues: map[uuid.UUID]uuid.UUID{}, comments: map[uuid.UUID]uuid.UUID{},
	}
}

// planOfItem finds the plan an imported item belongs to.
func (st *projectState) planOfItem(itemID uuid.UUID) (uuid.UUID, bool) {
	runID, ok := st.itemRuns[itemID]
	if !ok {
		return uuid.Nil, false
	}
	p, ok := st.runPlans[runID]
	return p, ok
}

type importedCase struct {
	id        uuid.UUID
	key       int
	sectionID uuid.UUID
	snapshot  repo.QARunItem // Title, Precondition, Steps, CaseVersion filled
}

func (im *Importer) importProject(ctx context.Context, lp legacyqa.Project, folderID *uuid.UUID) error {
	prep := newReport()
	err := repo.WithQAImportTx(ctx, im.scope, func(w *repo.QAImportWriter) error {
		if err := w.LockLegacy("projects", lp.ID); err != nil {
			return err
		}
		if _, done, err := w.Lookup("project_done", lp.ID); err != nil || done {
			if done {
				prep.skipped("projects", "already imported")
			}
			return err
		}
		slug, mapped := im.opt.ProjectsMap[lp.ID]
		if !mapped || slug == "" {
			slug, mapped = Slugify(lp.Title), false
		}
		pid, err := w.EnsureProject(slug, TruncateRunes(strings.TrimSpace(lp.Title), 200), folderID, mapped)
		if err != nil {
			return err
		}
		st := newProjectState(pid)
		if err := w.Remember("projects", lp.ID, pid); err != nil {
			return err
		}
		if err := im.importGuests(ctx, w, lp.ID, st, prep); err != nil {
			return fmt.Errorf("guests: %w", err)
		}
		if err := im.importLibrary(ctx, w, lp.ID, st, prep); err != nil {
			return fmt.Errorf("library: %w", err)
		}
		if err := im.importExecution(ctx, w, lp.ID, st, prep); err != nil {
			return fmt.Errorf("execution: %w", err)
		}
		if err := im.importIssues(ctx, w, lp.ID, st, prep); err != nil {
			return fmt.Errorf("issues: %w", err)
		}
		if err := w.Remember("project_done", lp.ID, pid); err != nil {
			return err
		}
		prep.ProjectsImported++
		if im.opt.DryRun {
			return errDryRun
		}
		return nil
	})
	if errors.Is(err, errDryRun) {
		err = nil
	}
	if err != nil {
		return err
	}
	im.rep.merge(prep)
	im.rep.ProjectsImported += prep.ProjectsImported
	if prep.ProjectsImported == 0 {
		im.rep.ProjectsSkipped++
	}
	return nil
}

func (im *Importer) importGuests(ctx context.Context, w *repo.QAImportWriter, legacyProject uuid.UUID, st *projectState, rep *Report) error {
	ids, err := im.legacy.ProjectAssignees(ctx, legacyProject)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if !im.guests[id] {
			continue // members, admins and owners see every project
		}
		u := im.user(&id)
		if u == nil {
			continue // dry run
		}
		if err := w.AddProjectMember(st.projectID, *u); err != nil {
			return err
		}
		rep.imported("project_members", 1)
	}
	return nil
}

func (im *Importer) importExecution(context.Context, *repo.QAImportWriter, uuid.UUID, *projectState, *Report) error {
	return nil
}

func (im *Importer) importIssues(context.Context, *repo.QAImportWriter, uuid.UUID, *projectState, *Report) error {
	return nil
}
