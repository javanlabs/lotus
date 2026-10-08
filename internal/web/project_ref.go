package web

import (
	"errors"
	"net/url"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/repo"
)

// Browser URLs name a project and a component by slug:
//
//	/projects/{project-slug}/…
//	/projects/{project-slug}/metrics/{kind}
//	/projects/{project-slug}/components/{component-slug}/…
//	/projects/{project-slug}/components/{component-slug}/metrics/{kind}/…
//
// Every link the app builds comes from the helpers below, never from
// concatenation at the call site, so the scheme lives in one place.
//
// The ids those URLs used to carry still work. A page fetched by id
// (/projects/{uuid}/…, and the retired /components/{uuid}/… and
// /p/{uuid}/c/{uuid}/m/{uuid}/…) answers 301 to its slug URL, keeping the
// rest of the path and the query — that is how links in mail already sent
// (run assignments, invitations) and bookmarks still land. Anything else
// (a form post, an htmx request) is served by id as before, so a page
// loaded before a deploy keeps working after it.
//
// A ref that parses as a UUID is an id, whatever else it might be: a slug
// shaped like a UUID loses to the id lookup. Slugs are editable, and there
// is no history of old ones: renaming a slug makes URLs carrying the old
// one 404.
//
// The JSON CRUD endpoints (projects.go, bindings.go) and the QA API keep
// taking ids only; they are not browser URLs.

// projectParam and componentParam are the route params a project and a
// component are named by under /projects. legacyComponentParam is the one
// the retired /components/:component_id routes use.
const (
	projectParam         = "id"
	componentParam       = "cid"
	legacyComponentParam = "component_id"
)

// Slugs these words would shadow a fixed route (/projects/new,
// /projects/move, /projects/:id/components/new), so they are refused on
// create and update. "." and ".." would be folded away by the browser.
var (
	reservedProjectSlugs   = []string{"new", "move", ".", ".."}
	reservedComponentSlugs = []string{"new", ".", ".."}
)

// isIDRef reports whether a URL ref is an id rather than a slug. Only the
// canonical 36-character form counts: uuid.Parse also takes 32 bare hex
// digits, which is a perfectly good slug (a commit hash, say).
func isIDRef(ref string) bool {
	if len(ref) != 36 {
		return false
	}
	_, err := uuid.Parse(ref)
	return err == nil
}

// slugRefusal says why slug cannot be used in a URL, or "" when it can.
func slugRefusal(slug string, reserved []string) string {
	if isIDRef(slug) {
		return "a slug cannot have the form of an id"
	}
	for _, r := range reserved {
		if strings.EqualFold(slug, r) {
			return "“" + slug + "” is reserved; choose another slug"
		}
	}
	return ""
}

// urlRef is what a URL names an entity by: its slug, escaped, unless the
// slug is one the router could not reach (a reserved word, or a slug that
// would read as an id) — those predate the refusal above, and fall back to
// the id so the link still opens the right page.
func urlRef(slug string, id uuid.UUID, reserved []string) string {
	if slug == "" || slugRefusal(slug, reserved) != "" {
		return id.String()
	}
	return url.PathEscape(slug)
}

func projectURL(p *repo.Project) string {
	return "/projects/" + urlRef(p.Slug, p.ID, reservedProjectSlugs)
}

// projectURLFor is projectURL for a row that carries only a project's id
// and slug.
func projectURLFor(id uuid.UUID, slug string) string {
	return projectURL(&repo.Project{ID: id, Slug: slug})
}

func componentURL(p *repo.Project, comp *repo.Component) string {
	return projectURL(p) + "/components/" + urlRef(comp.Slug, comp.ID, reservedComponentSlugs)
}

// metricURL is a component's page for one integration kind. A component
// has at most one live binding per kind (migration 00035), so the kind
// names it; the binding-id form it replaced redirects here
// (canonicalMetricURL).
func metricURL(p *repo.Project, comp *repo.Component, kind string) string {
	return componentURL(p, comp) + "/metrics/" + kind
}

// projectMetricURL is a project's page for one integration kind: every
// component's binding of that kind, side by side.
func projectMetricURL(p *repo.Project, kind string) string {
	return projectURL(p) + "/metrics/" + kind
}

func qaBase(p *repo.Project) string { return projectURL(p) + "/qa" }

func qaBaseFor(id uuid.UUID, slug string) string { return projectURLFor(id, slug) + "/qa" }

type projectRefKey string

type componentRefKey string

// lookupProject resolves a project ref from the URL — escaped, as it came
// — by id or by slug. A ref naming no project in this organization is
// 404. The result is kept for the rest of the request, so the access guard
// and the handler share one lookup.
func lookupProject(c *fiber.Ctx, ref string) (*repo.Project, error) {
	key := projectRefKey(ref)
	if p, ok := c.Locals(key).(*repo.Project); ok {
		return p, nil
	}
	s, err := mustScope(c)
	if err != nil {
		return nil, err
	}
	raw, err := url.PathUnescape(ref)
	if err != nil || raw == "" {
		return nil, fiber.ErrNotFound
	}
	var p *repo.Project
	if isIDRef(raw) {
		p, err = repo.ProjectByID(c.Context(), s, uuid.MustParse(raw))
	} else {
		p, err = repo.ProjectBySlug(c.Context(), s, raw)
	}
	if errors.Is(err, repo.ErrNotFound) {
		return nil, fiber.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	c.Locals(key, p)
	return p, nil
}

// resolveProject is the project a /projects/:id… route names.
func resolveProject(c *fiber.Ctx) (*repo.Project, error) {
	return lookupProject(c, c.Params(projectParam))
}

// lookupComponent resolves a component ref within project. A component of
// another project, named by id, is 404 like an unknown one.
func lookupComponent(c *fiber.Ctx, project *repo.Project, ref string) (*repo.Component, error) {
	key := componentRefKey(project.ID.String() + "/" + ref)
	if comp, ok := c.Locals(key).(*repo.Component); ok {
		return comp, nil
	}
	s, err := mustScope(c)
	if err != nil {
		return nil, err
	}
	raw, err := url.PathUnescape(ref)
	if err != nil || raw == "" {
		return nil, fiber.ErrNotFound
	}
	var comp *repo.Component
	if isIDRef(raw) {
		comp, err = repo.ComponentByID(c.Context(), s, uuid.MustParse(raw))
		if err == nil && comp.ProjectID != project.ID {
			err = repo.ErrNotFound
		}
	} else {
		comp, err = repo.ComponentBySlug(c.Context(), s, project.ID, raw)
	}
	if errors.Is(err, repo.ErrNotFound) {
		return nil, fiber.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	c.Locals(key, comp)
	return comp, nil
}

// resolveComponentRoute is the project and component a component route
// names: /projects/:id/components/:cid…, or the retired
// /components/:component_id…, which carries the component's id alone.
func resolveComponentRoute(c *fiber.Ctx) (*repo.Project, *repo.Component, error) {
	if ref := c.Params(legacyComponentParam); ref != "" {
		s, err := mustScope(c)
		if err != nil {
			return nil, nil, err
		}
		if !isIDRef(ref) {
			return nil, nil, fiber.ErrNotFound
		}
		comp, err := repo.ComponentByID(c.Context(), s, uuid.MustParse(ref))
		if err != nil {
			return nil, nil, notFoundOr(err)
		}
		project, err := repo.ProjectByID(c.Context(), s, comp.ProjectID)
		if err != nil {
			return nil, nil, notFoundOr(err)
		}
		return project, comp, nil
	}
	project, err := resolveProject(c)
	if err != nil {
		return nil, nil, err
	}
	comp, err := lookupComponent(c, project, c.Params(componentParam))
	if err != nil {
		return nil, nil, err
	}
	return project, comp, nil
}

func notFoundOr(err error) error {
	if errors.Is(err, repo.ErrNotFound) {
		return fiber.ErrNotFound
	}
	return err
}

// canonicalURL sends a page fetched by id to its slug URL. It sits in a
// route's chain after the access and permission guards, so a guest asking
// for a project it is not listed on gets the guard's 404, never a redirect
// that would show the project's slug. Only GET (and HEAD) is redirected:
// a form post or htmx call by id is served where it is.
func canonicalURL() fiber.Handler {
	return func(c *fiber.Ctx) error {
		if c.Method() != fiber.MethodGet && c.Method() != fiber.MethodHead {
			return c.Next()
		}
		pref, cref := c.Params(projectParam), c.Params(componentParam)
		if !isIDRef(pref) && !isIDRef(cref) {
			return c.Next()
		}
		project, err := resolveProject(c)
		if err != nil {
			return err
		}
		// /projects/{p}[/components/{c}]/…: swap the refs in place, so
		// whatever follows them is kept as it was.
		segs := strings.Split(c.Path(), "/")
		segs[2] = strings.TrimPrefix(projectURL(project), "/projects/")
		if cref != "" {
			comp, err := lookupComponent(c, project, cref)
			if err != nil {
				return err
			}
			segs[4] = urlRef(comp.Slug, comp.ID, reservedComponentSlugs)
		}
		to := strings.Join(segs, "/")
		if to == c.Path() {
			// An entity whose slug cannot be routed keeps its id URL.
			return c.Next()
		}
		return redirectPermanent(c, to)
	}
}

// redirectPermanent answers 301 to path, keeping the request's query.
func redirectPermanent(c *fiber.Ctx, path string) error {
	if q := c.Request().URI().QueryString(); len(q) > 0 {
		path += "?" + string(q)
	}
	return c.Redirect(path, fiber.StatusMovedPermanently)
}

// legacyComponentRedirect answers a GET of a retired /components/:component_id
// route with 301 to the same page under the component's slug URL; suffix
// is what follows the component in both.
func legacyComponentRedirect(suffix string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		project, comp, err := resolveComponentRoute(c)
		if err != nil {
			return err
		}
		return redirectPermanent(c, componentURL(project, comp)+suffix)
	}
}
