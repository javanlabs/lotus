package web

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/authz"
	"github.com/uyab/lotus/internal/repo"
	"github.com/uyab/lotus/internal/tenant"
)

// A project's tags, shown under its name on the project page
// (components/project_tags.html). Owners and admins (project.tag) add and
// remove them in the page's settings popover; everyone sees them. Both
// routes answer htmx with the re-rendered editor (plus the header's pills,
// out of band) and a plain post with a redirect back to the project.

// projectTagsData is what the project_tags partial renders. AllTags feeds
// the add box's suggestions; OOB adds the header's read-only pills to a
// route's response so they follow the change.
type projectTagsData struct {
	URL     string
	Tags    []repo.Tag
	AllTags []repo.Tag
	CanTag  bool
	Error   string
	OOB     bool
}

// loadProjectTags reads a project's tags and the organization's for the
// partial.
func loadProjectTags(c *fiber.Ctx, s tenant.Scope, project *repo.Project) (projectTagsData, error) {
	byProject, err := repo.ProjectTagsByProject(c.Context(), s)
	if err != nil {
		return projectTagsData{}, err
	}
	all, err := repo.ListTags(c.Context(), s)
	if err != nil {
		return projectTagsData{}, err
	}
	return projectTagsData{
		URL: projectURL(project), Tags: byProject[project.ID], AllTags: all,
		CanTag: mustPrincipal(c).Can("project.tag"),
	}, nil
}

// answerProjectTags ends a tag change: the partial for htmx (with status,
// so a refusal can carry its message), a redirect to the project otherwise.
func answerProjectTags(c *fiber.Ctx, d Deps, s tenant.Scope, project *repo.Project, status int, msg string) error {
	if c.Get("HX-Request") == "" {
		if msg != "" {
			return fiber.NewError(status, msg)
		}
		return c.Redirect(projectURL(project), fiber.StatusFound)
	}
	data, err := loadProjectTags(c, s, project)
	if err != nil {
		return writeRepoErr(c, err)
	}
	data.Error = msg
	data.OOB = true
	c.Type("html")
	c.Status(status)
	return d.Renderer.RenderNamed(c, "project_tags", data)
}

func auditProjectTag(c *fiber.Ctx, s tenant.Scope, action string, project *repo.Project, tag string) error {
	meta, _ := json.Marshal(fiber.Map{"tag": tag})
	return repo.RecordAuditEvent(c.Context(), s, auditActor(c), action, "project", &project.ID, meta)
}

func addProjectTagUI(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, err := mustScope(c)
		if err != nil {
			return err
		}
		project, err := resolveProject(c)
		if err != nil {
			return err
		}
		tag, err := repo.AddProjectTag(c.Context(), s, project.ID, c.FormValue("name"))
		if errors.Is(err, repo.ErrInvalidTag) {
			return answerProjectTags(c, d, s, project, fiber.StatusUnprocessableEntity, "Tag name must be 1–40 characters.")
		}
		if err != nil {
			return writeRepoErr(c, err)
		}
		if err := auditProjectTag(c, s, "project.tag.add", project, tag.Name); err != nil {
			return writeRepoErr(c, err)
		}
		return answerProjectTags(c, d, s, project, fiber.StatusOK, "")
	}
}

func removeProjectTagUI(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, err := mustScope(c)
		if err != nil {
			return err
		}
		project, err := resolveProject(c)
		if err != nil {
			return err
		}
		tagID, err := uuid.Parse(c.Params("tid"))
		if err != nil {
			return fiber.ErrNotFound
		}
		// The name, for the audit trail, before the tag may be deleted
		// along with its last project.
		byProject, err := repo.ProjectTagsByProject(c.Context(), s)
		if err != nil {
			return writeRepoErr(c, err)
		}
		name := ""
		for _, t := range byProject[project.ID] {
			if t.ID == tagID {
				name = t.Name
			}
		}
		if strings.TrimSpace(name) == "" {
			return fiber.ErrNotFound
		}
		if err := repo.RemoveProjectTag(c.Context(), s, project.ID, tagID); err != nil {
			return writeRepoErr(c, err)
		}
		if err := auditProjectTag(c, s, "project.tag.remove", project, name); err != nil {
			return writeRepoErr(c, err)
		}
		return answerProjectTags(c, d, s, project, fiber.StatusOK, "")
	}
}

func registerProjectTagRoutes(app *fiber.App, d Deps, requireSession, loadPrincipal fiber.Handler) {
	app.Post("/projects/:id/tags", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("project.tag"), addProjectTagUI(d))
	app.Post("/projects/:id/tags/:tid/remove", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("project.tag"), removeProjectTagUI(d))
}
