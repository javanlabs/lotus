package web

import (
	"bytes"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/uyab/lotus/internal/httpx"
)

// The info popover of the summary strip is positioned by Alpine
// (:style, object form — AGENTS.md) over static styles that make it a
// fixed, 280px, layered card. Swapping the two once rendered the Alpine
// object as CSS and left a bare `:` attribute that Alpine cannot parse,
// throwing on every page with the strip and dropping the popover's
// position, width and z-index.
func TestDashboardBlock_InfoPopoverStyles(t *testing.T) {
	r := httpx.NewRenderer(os.DirFS("../../web/templates"), true)
	var out bytes.Buffer
	if err := r.RenderNamed(&out, "dashboard_block", dashboardViewModel{ShowSummary: true}); err != nil {
		t.Fatal(err)
	}
	html := out.String()
	i := strings.Index(html, `x-ref="panel"`)
	if i < 0 {
		t.Fatal("no info popover rendered")
	}
	tag := html[strings.LastIndex(html[:i], "<"):]
	tag = tag[:strings.Index(tag, ">")+1]
	if regexp.MustCompile(`\s:(\s|=|>)`).MatchString(tag) {
		t.Errorf("popover has a bare ':' attribute: %s", tag)
	}
	if !strings.Contains(tag, `style="position:fixed;z-index:60;width:280px`) {
		t.Errorf("popover lost its static styles: %s", tag)
	}
	if !strings.Contains(tag, `:style="{ top: top + &#39;px&#39;, left: left + &#39;px&#39; }"`) && !strings.Contains(tag, `:style="{ top: top + 'px', left: left + 'px' }"`) {
		t.Errorf("popover is not positioned by an object-form :style: %s", tag)
	}
}
