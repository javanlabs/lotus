package web

import (
	"os"
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
	got := renderCard(t, map[string]any{"Variant": "inverse"})
	if !strings.Contains(got, `class="lotus-card-inverse"`) {
		t.Errorf("got %q, want the inverse class", got)
	}
	if strings.Contains(got, `"lotus-card "`) || strings.Contains(got, `class="lotus-card"`) {
		t.Errorf("inverse card also carried the glass class: %q", got)
	}
}

func TestCard_PaddingAndStylePassThrough(t *testing.T) {
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
	got := renderCard(t, map[string]any{"Class": "lotus-shape"})
	if !strings.Contains(got, `class="lotus-card lotus-shape"`) {
		t.Errorf("got %q", got)
	}
	if strings.Contains(got, "style=") {
		t.Errorf("class-only card emitted a style attribute: %q", got)
	}
}
