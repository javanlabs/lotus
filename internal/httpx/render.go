// Package httpx renders html/template pages against a layout.
//
// Templates are parsed once from the embedded filesystem in production;
// task 4.1 extends this with RenderPartial (htmx fragment rendering without
// the layout) and an Org field on ViewData once the tenant plane exists.
// This first cut only needs to prove the embed-vs-disk toggle and one
// working page, per task 0.1's acceptance criteria.
package httpx

import (
	"html/template"
	"io"
	"io/fs"
	"path"
)

// ViewData is passed to every page template.
type ViewData struct {
	Title string
	Data  any
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

func (r *Renderer) parse(page string) (*template.Template, error) {
	tmpl := template.New("layout")
	layout, err := fs.ReadFile(r.fsys, path.Join("layout", "layout.html"))
	if err != nil {
		return nil, err
	}
	if _, err := tmpl.Parse(string(layout)); err != nil {
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
