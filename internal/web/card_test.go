package web

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/uyab/lotus/internal/httpx"
)

// lotus_card is the Go-template port of the design system's
// components/core/Card.jsx. These pin the parts of that contract a
// template can get silently wrong: the variant picking the right class,
// the default padding coming from CSS rather than markup, and the style
// passthrough surviving html/template's CSS sanitizer — which rejects a
// declaration list in a style attribute and renders ZgotmplZ instead.
func renderCard(t *testing.T, args map[string]any) string {
	t.Helper()
	r := httpx.NewRenderer(os.DirFS("../../web/templates"), true)
	var sb strings.Builder
	if err := r.RenderNamed(&sb, "lotus_card_open", args); err != nil {
		t.Fatalf("render: %v", err)
	}
	return strings.TrimSpace(sb.String())
}

func TestCard_GlassIsTheDefaultVariant(t *testing.T) {
	t.Parallel()
	got := renderCard(t, map[string]any{})
	if !strings.Contains(got, `class="lotus-card"`) {
		t.Errorf("got %q, want the glass class", got)
	}
	// No padding argument means no style attribute at all: the 13px/14px
	// default belongs to the stylesheet, not to every call site.
	if strings.Contains(got, "style=") {
		t.Errorf("bare card emitted a style attribute: %q", got)
	}
}

func TestCard_InverseVariant(t *testing.T) {
	t.Parallel()
	got := renderCard(t, map[string]any{"Variant": "inverse"})
	if !strings.Contains(got, `class="lotus-card-inverse"`) {
		t.Errorf("got %q, want the inverse class", got)
	}
	if strings.Contains(got, `"lotus-card "`) || strings.Contains(got, `class="lotus-card"`) {
		t.Errorf("inverse card also carried the glass class: %q", got)
	}
}

func TestCard_PaddingAndStylePassThrough(t *testing.T) {
	t.Parallel()
	got := renderCard(t, map[string]any{
		"Padding": "20px 22px",
		"Class":   "lotus-shape",
		"Style":   "max-width:560px;display:flex;flex-direction:column;gap:16px",
	})
	for _, want := range []string{
		`class="lotus-card lotus-shape"`,
		"padding:20px 22px;",
		"max-width:560px",
		"flex-direction:column",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
	// The failure mode this guards: html/template replaces a style value it
	// cannot parse with ZgotmplZ, silently dropping the layout.
	if strings.Contains(got, "ZgotmplZ") {
		t.Errorf("style was sanitized away: %q", got)
	}
}

// A card with only extra classes and no padding must still skip the style
// attribute — the branch that builds it keys off Padding OR Style.
func TestCard_ClassOnlyEmitsNoStyle(t *testing.T) {
	t.Parallel()
	got := renderCard(t, map[string]any{"Class": "lotus-shape"})
	if !strings.Contains(got, `class="lotus-card lotus-shape"`) {
		t.Errorf("got %q", got)
	}
	if strings.Contains(got, "style=") {
		t.Errorf("class-only card emitted a style attribute: %q", got)
	}
}

// A card argument is a Go string literal, so a {{...}} action written
// inside one is inert text, not a substitution. That failure is silent and
// looks like a styling bug: --lotus-row-grid once shipped as the literal
// "{{.GridTemplate}}", which dropped the projects table onto its fallback
// grid and left every row misaligned with its own header. Dynamic values
// have to be built with printf instead.
func TestCardArgs_HaveNoUnevaluatedActions(t *testing.T) {
	t.Parallel()
	call := regexp.MustCompile(`(?s)\{\{template "lotus_card[a-z_]*_open" \(dict(.*?)\)\}\}`)
	quoted := regexp.MustCompile(`"(Padding|Class|Style|Variant)" "([^"]*)"`)
	backtick := regexp.MustCompile("\"Attrs\" `([^`]*)`")

	err := filepath.WalkDir("../../web/templates", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".html") {
			return err
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, c := range call.FindAllStringSubmatch(string(body), -1) {
			for _, a := range quoted.FindAllStringSubmatch(c[1], -1) {
				if strings.Contains(a[2], "{{") {
					t.Errorf("%s: %s is a literal containing a template action: %q — build it with printf",
						filepath.Base(path), a[1], a[2])
				}
			}
			for _, a := range backtick.FindAllStringSubmatch(c[1], -1) {
				if strings.Contains(a[1], "{{") {
					t.Errorf("%s: Attrs is a literal containing a template action: %q — build it with printf",
						filepath.Base(path), a[1])
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
