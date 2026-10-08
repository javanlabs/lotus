package web

import (
	"reflect"
	"testing"

	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/httpx"
	"github.com/uyab/lotus/internal/repo"
)

// A component's page for a kind sits under the project's page for that
// kind: the project's SonarQube page, then the component. Reached from
// either the project's SonarQube page or the project page, the trail is
// the same, and the SonarQube crumb leads back to every component's
// SonarQube.
func TestMetricTrail_KindBeforeComponent(t *testing.T) {
	p := &repo.Project{ID: uuid.New(), Slug: "alurkerja", Name: "AlurKerja"}
	c := &repo.Component{ID: uuid.New(), Slug: "authentication-service", Name: "Authentication"}

	project := projectKindTrail(nil, p, "sonarqube")
	if want := []httpx.NavCrumb{
		{Label: "AlurKerja", Href: "/projects/alurkerja"},
		{Label: "SonarQube"},
	}; !reflect.DeepEqual(project, want) {
		t.Fatalf("project SonarQube trail = %+v, want %+v", project, want)
	}

	got := metricTrail(nil, p, c, "sonarqube")
	if want := []httpx.NavCrumb{
		{Label: "AlurKerja", Href: "/projects/alurkerja"},
		{Label: "SonarQube", Href: "/projects/alurkerja/metrics/sonarqube"},
		{Label: "Authentication"},
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("component SonarQube trail = %+v, want %+v", got, want)
	}
	// The project page's trail is a prefix of the component's, label for
	// label: going down from it only adds the component.
	for i := range project {
		if project[i].Label != got[i].Label {
			t.Errorf("crumb %d: project page %q, component page %q", i, project[i].Label, got[i].Label)
		}
	}

	syncs := metricTrail(nil, p, c, "sonarqube", httpx.NavCrumb{Label: "Syncs"})
	if want := []httpx.NavCrumb{
		{Label: "AlurKerja", Href: "/projects/alurkerja"},
		{Label: "SonarQube", Href: "/projects/alurkerja/metrics/sonarqube"},
		{Label: "Authentication", Href: "/projects/alurkerja/components/authentication-service/metrics/sonarqube"},
		{Label: "Syncs"},
	}; !reflect.DeepEqual(syncs, want) {
		t.Fatalf("Syncs trail = %+v, want %+v", syncs, want)
	}

	// The trail is a fresh slice each time: marking the kind crumb a link
	// must not leak into the project page's own trail.
	if again := projectKindTrail(nil, p, "sonarqube"); again[len(again)-1].Href != "" {
		t.Errorf("project trail's kind crumb became a link: %+v", again)
	}
}
