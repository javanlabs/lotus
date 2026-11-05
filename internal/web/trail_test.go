package web

import (
	"reflect"
	"testing"

	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/httpx"
	"github.com/uyab/lotus/internal/repo"
)

// A component's page for a kind reads project → component → kind, the
// way the page itself is built (the component's header, then its tab for
// the kind). The project's page for the kind is project → kind; picking a
// component there only puts the component between them.
func TestMetricTrail_ProjectThenComponentThenKind(t *testing.T) {
	t.Parallel()
	p := &repo.Project{ID: uuid.New(), Slug: "alurkerja", Name: "AlurKerja"}
	c := &repo.Component{ID: uuid.New(), Slug: "authentication-service", Name: "Authentication"}

	project := projectKindTrail(p, "sonarqube", trailMenus{})
	if want := []httpx.NavCrumb{
		{Label: "AlurKerja", Href: "/projects/alurkerja"},
		{Label: "SonarQube"},
	}; !reflect.DeepEqual(project, want) {
		t.Fatalf("project SonarQube trail = %+v, want %+v", project, want)
	}

	got := metricTrail(p, c, "sonarqube", trailMenus{})
	if want := []httpx.NavCrumb{
		{Label: "AlurKerja", Href: "/projects/alurkerja"},
		{Label: "Authentication", Href: "/projects/alurkerja/components/authentication-service"},
		{Label: "SonarQube"},
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("component SonarQube trail = %+v, want %+v", got, want)
	}

	syncs := metricTrail(p, c, "sonarqube", trailMenus{}, httpx.NavCrumb{Label: "Syncs"})
	if want := []httpx.NavCrumb{
		{Label: "AlurKerja", Href: "/projects/alurkerja"},
		{Label: "Authentication", Href: "/projects/alurkerja/components/authentication-service"},
		{Label: "SonarQube", Href: "/projects/alurkerja/components/authentication-service/metrics/sonarqube"},
		{Label: "Syncs"},
	}; !reflect.DeepEqual(syncs, want) {
		t.Fatalf("Syncs trail = %+v, want %+v", syncs, want)
	}
}

// The project and component crumbs carry their dropdowns: switching
// project or component from a page for a kind stays on that kind, an
// unbound component falls back to its own page and says why, and "All
// components" leads back to the project's page for the kind.
func TestTrailMenus_SwitchStaysOnTheKind(t *testing.T) {
	t.Parallel()
	ak := repo.Project{ID: uuid.New(), Slug: "alurkerja", Name: "AlurKerja"}
	sq := repo.Project{ID: uuid.New(), Slug: "syarq", Name: "SyarQ"}
	auth := repo.Component{ID: uuid.New(), Slug: "auth", Name: "authentication"}
	bpm := repo.Component{ID: uuid.New(), Slug: "bpm", Name: "BPM"}
	camunda := repo.Component{ID: uuid.New(), Slug: "camunda", Name: "Camunda"}

	if got, want := projectMenu([]repo.Project{ak, sq}, &ak, "sonarqube"), []httpx.NavMenuItem{
		{Label: "AlurKerja", Href: "/projects/alurkerja/metrics/sonarqube", Current: true},
		{Label: "SyarQ", Href: "/projects/syarq/metrics/sonarqube"},
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("project menu = %+v, want %+v", got, want)
	}
	if got := projectMenu([]repo.Project{ak, sq}, &ak, ""); got[1].Href != "/projects/syarq" {
		t.Errorf("outside a kind, a project opens its own page; got %q", got[1].Href)
	}

	got := componentMenu(&ak, []repo.Component{camunda, bpm, auth}, &bpm, "sonarqube",
		map[uuid.UUID]bool{auth.ID: true, bpm.ID: true})
	if want := []httpx.NavMenuItem{
		{Label: "All components", Href: "/projects/alurkerja/metrics/sonarqube", Sep: true},
		{Label: "authentication", Href: "/projects/alurkerja/components/auth/metrics/sonarqube"},
		{Label: "BPM", Href: "/projects/alurkerja/components/bpm/metrics/sonarqube", Current: true},
		{Label: "Camunda", Href: "/projects/alurkerja/components/camunda", Hint: "no SonarQube"},
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("component menu = %+v, want %+v", got, want)
	}

	trail := metricTrail(&ak, &bpm, "sonarqube", trailMenus{Projects: []httpx.NavMenuItem{{Label: "x"}}, Components: got})
	if len(trail[0].Menu) != 1 || len(trail[1].Menu) != 4 || trail[2].Menu != nil {
		t.Errorf("menus on the wrong crumbs: %+v", trail)
	}
}
