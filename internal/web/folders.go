package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/authz"
	"github.com/uyab/lotus/internal/repo"
	"github.com/uyab/lotus/internal/tenant"
)

// Folder routes are form posts, like every other screen action in the app:
// a browser form cannot PATCH or DELETE. Each one answers a rejection the
// way rejectInviteForm does — htmx gets the message inside the still-open
// dialog, a plain post gets the status — and a success with a flash and
// redirectAfterMutation.

// folderFormError answers a rejected folder form.
func folderFormError(c *fiber.Ctx, d Deps, status int, msg string) error {
	if c.Get("HX-Request") == "" {
		return fiber.NewError(status, msg)
	}
	c.Type("html")
	return d.Renderer.RenderNamed(c, "form_error", msg)
}

// folderErr turns a repo error into the status and sentence a person sees.
// name is the folder name a name clash should be reported with.
func folderErr(err error, name string) (int, string, bool) {
	var notEmpty *repo.FolderNotEmptyError
	switch {
	case errors.As(err, &notEmpty):
		return fiber.StatusConflict, notEmpty.Error(), true
	case errors.Is(err, repo.ErrFolderCycle):
		return fiber.StatusConflict, "Can't move a folder into itself.", true
	case errors.Is(err, repo.ErrFolderTooDeep):
		return fiber.StatusConflict, fmt.Sprintf("Folders can nest at most %d levels.", repo.MaxFolderDepth), true
	case errors.Is(err, repo.ErrFolderNameTaken):
		return fiber.StatusConflict, fmt.Sprintf("There's already a folder called '%s' here.", name), true
	case errors.Is(err, repo.ErrInvalidInput):
		return fiber.StatusUnprocessableEntity,
			fmt.Sprintf("Folder name is required and can be at most %d characters.", repo.MaxFolderNameLength), true
	case errors.Is(err, repo.ErrNotFound):
		return fiber.StatusNotFound, "That folder no longer exists.", true
	}
	return 0, "", false
}

// optionalUUID reads a picker value: "" is the top level (nil).
func optionalUUID(v string) (*uuid.UUID, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil, nil
	}
	id, err := uuid.Parse(v)
	if err != nil {
		return nil, err
	}
	return &id, nil
}

func uuidString(id *uuid.UUID) string {
	if id == nil {
		return ""
	}
	return id.String()
}

func folderAuditActor(c *fiber.Ctx) *uuid.UUID {
	if p, ok := authz.CurrentPrincipal(c); ok && p.UserID != uuid.Nil {
		id := p.UserID
		return &id
	}
	return nil
}

func auditFolder(c *fiber.Ctx, s tenant.Scope, action string, id uuid.UUID, meta fiber.Map) error {
	raw, _ := json.Marshal(meta)
	return repo.RecordAuditEvent(c.Context(), s, folderAuditActor(c), action, "folder", &id, raw)
}

func createFolderUI(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, err := mustScope(c)
		if err != nil {
			return err
		}
		name := c.FormValue("name")
		parentID, err := optionalUUID(c.FormValue("parent_id"))
		if err != nil {
			return folderFormError(c, d, fiber.StatusNotFound, "That folder no longer exists.")
		}
		f, err := repo.CreateFolder(c.Context(), s, name, parentID)
		if err != nil {
			if status, msg, ok := folderErr(err, strings.TrimSpace(name)); ok {
				return folderFormError(c, d, status, msg)
			}
			return err
		}
		if err := auditFolder(c, s, "folder.create", f.ID, fiber.Map{"name": f.Name, "parent_id": uuidString(f.ParentID)}); err != nil {
			return writeRepoErr(c, err)
		}
		setFlash(c, "folder.created", f.Name)
		return redirectAfterMutation(c, "/projects")
	}
}

func renameFolderUI(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, err := mustScope(c)
		if err != nil {
			return err
		}
		id, err := uuid.Parse(c.Params("id"))
		if err != nil {
			return fiber.ErrNotFound
		}
		before, err := repo.FolderByID(c.Context(), s, id)
		if err != nil {
			if status, msg, ok := folderErr(err, ""); ok {
				return folderFormError(c, d, status, msg)
			}
			return err
		}
		name := c.FormValue("name")
		f, err := repo.RenameFolder(c.Context(), s, id, name)
		if err != nil {
			if status, msg, ok := folderErr(err, strings.TrimSpace(name)); ok {
				return folderFormError(c, d, status, msg)
			}
			return err
		}
		if err := auditFolder(c, s, "folder.rename", f.ID, fiber.Map{"from": before.Name, "to": f.Name}); err != nil {
			return writeRepoErr(c, err)
		}
		setFlash(c, "folder.renamed", f.Name)
		return redirectAfterMutation(c, "/projects")
	}
}

func moveFolderUI(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, err := mustScope(c)
		if err != nil {
			return err
		}
		id, err := uuid.Parse(c.Params("id"))
		if err != nil {
			return fiber.ErrNotFound
		}
		before, err := repo.FolderByID(c.Context(), s, id)
		if err != nil {
			if status, msg, ok := folderErr(err, ""); ok {
				return folderFormError(c, d, status, msg)
			}
			return err
		}
		parentID, err := optionalUUID(c.FormValue("parent_id"))
		if err != nil {
			return folderFormError(c, d, fiber.StatusNotFound, "That folder no longer exists.")
		}
		f, err := repo.MoveFolder(c.Context(), s, id, parentID)
		if err != nil {
			if status, msg, ok := folderErr(err, before.Name); ok {
				return folderFormError(c, d, status, msg)
			}
			return err
		}
		if err := auditFolder(c, s, "folder.move", f.ID, fiber.Map{
			"from_parent": uuidString(before.ParentID), "to_parent": uuidString(f.ParentID),
		}); err != nil {
			return writeRepoErr(c, err)
		}
		setFlash(c, "folder.moved", f.Name)
		return redirectAfterMutation(c, "/projects")
	}
}

func deleteFolderUI(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, err := mustScope(c)
		if err != nil {
			return err
		}
		id, err := uuid.Parse(c.Params("id"))
		if err != nil {
			return fiber.ErrNotFound
		}
		f, err := repo.DeleteFolder(c.Context(), s, id)
		if err != nil {
			if status, msg, ok := folderErr(err, ""); ok {
				return folderFormError(c, d, status, msg)
			}
			return err
		}
		if err := auditFolder(c, s, "folder.delete", f.ID, fiber.Map{"name": f.Name}); err != nil {
			return writeRepoErr(c, err)
		}
		setFlash(c, "folder.deleted", f.Name)
		return redirectAfterMutation(c, "/projects")
	}
}

func moveProjectUI(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, err := mustScope(c)
		if err != nil {
			return err
		}
		id, err := uuid.Parse(c.Params("id"))
		if err != nil {
			return fiber.ErrNotFound
		}
		folderID, err := optionalUUID(c.FormValue("folder_id"))
		if err != nil {
			return folderFormError(c, d, fiber.StatusNotFound, "That folder no longer exists.")
		}
		from, err := repo.MoveProject(c.Context(), s, id, folderID)
		if err != nil {
			if errors.Is(err, repo.ErrNotFound) {
				return folderFormError(c, d, fiber.StatusNotFound, "That project or folder no longer exists.")
			}
			return err
		}
		raw, _ := json.Marshal(fiber.Map{"from_folder": uuidString(from), "to_folder": uuidString(folderID)})
		if err := repo.RecordAuditEvent(c.Context(), s, folderAuditActor(c), "project.move", "project", &id, raw); err != nil {
			return writeRepoErr(c, err)
		}
		setFlash(c, "project.moved")
		return redirectAfterMutation(c, "/projects/"+id.String())
	}
}

// moveProjectsUI files every selected project (form field project_id,
// repeated) in one folder at once — the /projects page's multi-select.
// The repo moves the batch in one transaction, so it lands whole or not at
// all; each project still gets its own project.move audit event.
func moveProjectsUI(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, err := mustScope(c)
		if err != nil {
			return err
		}
		const gone = "That project or folder no longer exists."
		raw := c.Context().PostArgs().PeekMulti("project_id")
		if len(raw) == 0 {
			return folderFormError(c, d, fiber.StatusUnprocessableEntity, "Select at least one project.")
		}
		ids := make([]uuid.UUID, 0, len(raw))
		for _, r := range raw {
			id, err := uuid.Parse(string(r))
			if err != nil {
				return folderFormError(c, d, fiber.StatusNotFound, gone)
			}
			ids = append(ids, id)
		}
		folderID, err := optionalUUID(c.FormValue("folder_id"))
		if err != nil {
			return folderFormError(c, d, fiber.StatusNotFound, gone)
		}
		from, err := repo.MoveProjects(c.Context(), s, ids, folderID)
		if err != nil {
			if errors.Is(err, repo.ErrNotFound) {
				return folderFormError(c, d, fiber.StatusNotFound, gone)
			}
			return err
		}
		for id, was := range from {
			meta, _ := json.Marshal(fiber.Map{"from_folder": uuidString(was), "to_folder": uuidString(folderID)})
			if err := repo.RecordAuditEvent(c.Context(), s, folderAuditActor(c), "project.move", "project", &id, meta); err != nil {
				return writeRepoErr(c, err)
			}
		}
		if len(from) == 1 {
			setFlash(c, "project.moved")
		} else {
			setFlash(c, "projects.moved", strconv.Itoa(len(from)))
		}
		return redirectAfterMutation(c, "/projects")
	}
}

func registerFolderRoutes(app *fiber.App, d Deps, requireSession, loadPrincipal fiber.Handler) {
	app.Post("/folders", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("folder.create"), createFolderUI(d))
	app.Post("/folders/:id/rename", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("folder.update"), renameFolderUI(d))
	app.Post("/folders/:id/move", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("folder.update"), moveFolderUI(d))
	app.Post("/folders/:id/delete", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("folder.delete"), deleteFolderUI(d))
	app.Post("/projects/move", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("project.update"), moveProjectsUI(d))
	app.Post("/projects/:id/move", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("project.update"), moveProjectUI(d))
}
