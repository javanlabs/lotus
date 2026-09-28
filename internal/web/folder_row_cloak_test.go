package web

import (
	"bytes"
	"os"
	"regexp"
	"testing"

	"github.com/uyab/lotus/internal/httpx"
)

// The projects page renders the level's rows and, separately, the Failing
// list (failing projects at any depth, with where they live). The page
// opens on "all", so the Failing rows must be hidden from the first paint
// — not only once Alpine starts, or every failing project flashes up a
// second time, labelled "Top level" or with its folder path.
func TestFolderViewRow_FailingListHiddenBeforeScripts(t *testing.T) {
	r := httpx.NewRenderer(os.DirFS("../../web/templates"), true)
	row := func(list string) string {
		var out bytes.Buffer
		if err := r.RenderNamed(&out, "folder_view_row", map[string]any{
			"R": dashboardRow{Name: "AlurKerja", NodeID: "p1"}, "VM": dashboardViewModel{}, "List": list,
		}); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}
	opening := regexp.MustCompile(`(?s)<div class="lotus-repo-row[^>]*>`)
	if tag := opening.FindString(row("fail")); !regexp.MustCompile(`\sx-cloak[\s>]`).MatchString(tag) {
		t.Errorf("a Failing-list row is visible before scripts run: %s", tag)
	}
	if tag := opening.FindString(row("all")); regexp.MustCompile(`\sx-cloak[\s>]`).MatchString(tag) {
		t.Errorf("a row of the opening view is hidden: %s", tag)
	}
}
