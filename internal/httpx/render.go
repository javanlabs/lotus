// Package httpx renders html/template pages against a layout.
//
// Templates are re-parsed on every Render call — cheap enough at Lotus's
// scale, and it's what makes LOTUS_DEV=1 (os.DirFS instead of the embedded
// build) a genuine hot-reload rather than a restart. A page ("pages/x.html")
// is parsed together with layout/layout.html and every components/*.html
// (task 4.1's app-shell partials — sidebar, breadcrumb, logo, badges),
// so a page's {{define "content"}} can freely {{template}} any of them.
// RenderPartial renders a partials/*.html fragment standalone, without the
// layout, for htmx responses.
package httpx

import (
	"errors"
	"html/template"
	"io"
	"io/fs"
	"path"
	"strings"
)

// ViewData is passed to every page template.
type ViewData struct {
	Title string
	Data  any

	// Org, User, and Nav are set by every tenant-host authenticated page
	// (task 4.1) so the app-shell components (sidebar, user box,
	// breadcrumb) have what they need without each handler re-deriving it
	// into Data. Nil on auth-flow and apex pages, which render without
	// the shell.
	Org  *Org
	User *User
	Nav  *Nav

	// Flash is a one-shot success message for the action that redirected
	// here, rendered by the app shell as a toast. Nil on almost every
	// render: it is set only by the request immediately following a
	// mutation, and reading it clears it. See internal/web/flash.go.
	Flash *Flash
}

// Flash is a resolved one-shot message. Tone is a status key the toast
// styles from ("pass", "warn", "fail"), matching .lotus-status-dot-*.
//
// Handlers never build one directly — they name a message code and the
// web layer resolves it, so the copy stays in Go where the product's
// voice rules apply and a tampered cookie can only select a message
// Lotus already ships.
type Flash struct {
	Tone string
	Text string
}

// Org is the current organization, for the app shell's breadcrumb.
type Org struct {
	Name      string
	Subdomain string
}

// User is the current signed-in user, for the app shell's sidebar user box.
type User struct {
	Name  string
	Email string
	Role  string
}

// Nav is the app shell's sidebar + breadcrumb state for the current page.
// NavCrumb is one breadcrumb segment. Href empty means "current page".
type NavCrumb struct {
	Label string
	Href  string
}

type Nav struct {
	// Active is the Key of the NavItem to highlight.
	Active string
	Items  []NavItem
	// FooterItems pin to the bottom of the sidebar, above the wordmark,
	// separated from Items by the flexible gap. Help-style entries belong
	// here: they are always reachable but are not part of the product's
	// own navigation, and putting them last in Items only pushes them
	// under whatever section happens to be longest.
	FooterItems []NavItem
	// Trail is the breadcrumb path below the organization, outermost
	// first. Every segment but the last links; the last is the current
	// page and stays plain. A single-element Trail is the common case
	// (Settings), a project page adds one level, a component two.
	Trail []NavCrumb
}

// NavItem is one sidebar link. Href empty means "not built yet" — the
// shell renders it disabled rather than as a dead link.
type NavItem struct {
	Key   string
	Label string
	Href  string
	Count string
	Icon  template.HTML
}

// Initials returns up to two uppercase initials for the sidebar avatar
// badge (e.g. "Rizky Hartono" -> "RH"), falling back to the first letter
// of Email when Name is blank.
func (u User) Initials() string {
	fields := strings.Fields(u.Name)
	switch {
	case len(fields) >= 2:
		return strings.ToUpper(string([]rune(fields[0])[:1]) + string([]rune(fields[1])[:1]))
	case len(fields) == 1:
		r := []rune(fields[0])
		if len(r) >= 2 {
			return strings.ToUpper(string(r[:2]))
		}
		return strings.ToUpper(string(r))
	case u.Email != "":
		return strings.ToUpper(u.Email[:1])
	default:
		return "?"
	}
}

// Renderer parses and renders html/template pages against layout.html.
//
// fsys is rooted at the templates directory (containing layout/ and
// pages/). Pass an embedded fs.FS in production and os.DirFS(...) in dev
// (LOTUS_DEV=1) so templates re-parse from disk on every render.
type Renderer struct {
	fsys fs.FS
	dev  bool
}

func NewRenderer(fsys fs.FS, dev bool) *Renderer {
	return &Renderer{fsys: fsys, dev: dev}
}

// Render writes the named page ("pages/home.html") laid out inside
// layout.html to w.
func (r *Renderer) Render(w io.Writer, page string, vd ViewData) error {
	tmpl, err := r.parse(page)
	if err != nil {
		return err
	}
	return tmpl.ExecuteTemplate(w, "layout", vd)
}

// RenderPartial writes the named partial ("partials/slug_check.html")
// standalone, without the layout — for htmx fragment responses (task
// 1.6's slug-availability check is the first consumer; task 4.1 broadens
// this pattern to every htmx endpoint).
func (r *Renderer) RenderPartial(w io.Writer, name string, data any) error {
	body, err := fs.ReadFile(r.fsys, name)
	if err != nil {
		return err
	}
	tmpl, err := template.New(path.Base(name)).Funcs(funcMap).Parse(string(body))
	if err != nil {
		return err
	}
	return tmpl.Execute(w, data)
}

// RenderNamed executes one components/*.html definition standalone,
// without the layout — task 4.6's chart grid is the first consumer: it
// needs to be both part of a full page's first render (via
// {{template "name" .}} from that page, once parseComponents has
// registered it) and, unchanged, the htmx swap target when its own range
// selector changes. RenderPartial can't serve the second case here since
// it parses only the one named file in isolation, with no access to
// component definitions registered from other files — this instead runs
// the same layout+components parse Render itself does, then executes the
// requested definition directly instead of "layout".
func (r *Renderer) RenderNamed(w io.Writer, name string, data any) error {
	tmpl := template.New(name).Funcs(funcMap)
	if err := r.parseComponents(tmpl); err != nil {
		return err
	}
	return tmpl.ExecuteTemplate(w, name, data)
}

func (r *Renderer) parse(page string) (*template.Template, error) {
	tmpl := template.New("layout").Funcs(funcMap)
	layout, err := fs.ReadFile(r.fsys, path.Join("layout", "layout.html"))
	if err != nil {
		return nil, err
	}
	if _, err := tmpl.Parse(string(layout)); err != nil {
		return nil, err
	}
	// components/*.html (task 4.1) are the app shell's reusable pieces —
	// sidebar, breadcrumb, logo, status badge — each wrapped in its own
	// {{define "name"}}, so parsing them here just registers named
	// templates a page can {{template "name" .}} into itself; unlike
	// partials/ (RenderPartial's raw htmx fragments, no {{define}}
	// wrapper), components are never rendered standalone.
	if err := r.parseComponents(tmpl); err != nil {
		return nil, err
	}
	body, err := fs.ReadFile(r.fsys, page)
	if err != nil {
		return nil, err
	}
	// The page template's own {{define "content"}} overrides the layout's
	// placeholder block of the same name.
	if _, err := tmpl.Parse(string(body)); err != nil {
		return nil, err
	}
	return tmpl, nil
}

func (r *Renderer) parseComponents(tmpl *template.Template) error {
	entries, err := fs.ReadDir(r.fsys, "components")
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".html") {
			continue
		}
		body, err := fs.ReadFile(r.fsys, path.Join("components", e.Name()))
		if err != nil {
			return err
		}
		if _, err := tmpl.Parse(string(body)); err != nil {
			return err
		}
	}
	return nil
}
