package web

import (
	"testing"

	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/repo"
)

var (
	tagProject = repo.Tag{ID: uuid.New(), Name: "PROJECT"}
	tagProduct = repo.Tag{ID: uuid.New(), Name: "PRODUCT"}
)

// An old bookmark, a typo or a tag removed since must not leave the page
// filtering to nothing with no way out: unknown names drop, and with none
// left the page is All.
func TestBuildTagFilter_UnknownNamesFallBackToAll(t *testing.T) {
	t.Parallel()
	f := buildTagFilter("/dashboard", []string{"nope"}, []repo.Tag{tagProduct, tagProject})
	if len(f.Selected) != 0 {
		t.Fatalf("Selected = %v, want none", f.Selected)
	}
	if !f.All.Active || f.All.Href != "/dashboard" {
		t.Fatalf("All = %+v, want active at /dashboard", f.All)
	}
	for _, c := range f.Chips {
		if c.Active {
			t.Errorf("chip %+v is active", c)
		}
	}
}

func TestBuildTagFilter_MatchesIgnoringCaseAndTogglesInTheURL(t *testing.T) {
	t.Parallel()
	f := buildTagFilter("/dashboard", []string{"project", "PROJECT"}, []repo.Tag{tagProject, tagProduct})
	if len(f.Selected) != 1 || f.Selected[0] != tagProject {
		t.Fatalf("Selected = %v, want [PROJECT] (matched ignoring case, duplicates collapsed)", f.Selected)
	}
	if f.All.Active || f.All.Href != "/dashboard" {
		t.Fatalf("All = %+v, want inactive at /dashboard", f.All)
	}
	chips := map[string]tagChip{}
	for _, c := range f.Chips {
		chips[c.Name] = c
	}
	if len(f.Chips) != 2 || f.Chips[0].Name != "PROJECT" || f.Chips[1].Name != "PRODUCT" {
		t.Fatalf("chips = %+v, want PROJECT, PRODUCT in tags order", f.Chips)
	}
	if c := chips["PROJECT"]; !c.Active || c.Href != "/dashboard" {
		t.Errorf("PROJECT chip = %+v, want active, toggling off to /dashboard", c)
	}
	// Toggling PRODUCT on keeps PROJECT; the URL lists them in tags order.
	if c := chips["PRODUCT"]; c.Active || c.Href != "/dashboard?tag=PROJECT&tag=PRODUCT" {
		t.Errorf("PRODUCT chip = %+v, want inactive, toggling on to /dashboard?tag=PROJECT&tag=PRODUCT", c)
	}
}

// The dropdown's button names the selection, so the filter in force is
// readable without opening it.
func TestBuildTagFilter_LabelNamesTheSelection(t *testing.T) {
	t.Parallel()
	third := repo.Tag{ID: uuid.New(), Name: "XLSMART"}
	tags := []repo.Tag{tagProject, tagProduct, third}
	for _, tc := range []struct {
		raw  []string
		want string
	}{
		{nil, "All tags"},
		{[]string{"nope"}, "All tags"},
		{[]string{"product"}, "PRODUCT"},
		{[]string{"PRODUCT", "PROJECT"}, "PROJECT, PRODUCT"},
		{[]string{"PRODUCT", "PROJECT", "xlsmart"}, "3 tags"},
	} {
		f := buildTagFilter("/projects", tc.raw, tags)
		if f.Label != tc.want {
			t.Errorf("raw %v: Label = %q, want %q", tc.raw, f.Label, tc.want)
		}
		if f.Path != "/projects" {
			t.Errorf("Path = %q, want /projects", f.Path)
		}
	}
}

func TestBuildTagFilter_EscapesNamesInTheURL(t *testing.T) {
	t.Parallel()
	odd := repo.Tag{ID: uuid.New(), Name: "R&D team"}
	f := buildTagFilter("/projects", nil, []repo.Tag{odd})
	if got, want := f.Chips[0].Href, "/projects?tag=R%26D+team"; got != want {
		t.Fatalf("Href = %q, want %q", got, want)
	}
}
