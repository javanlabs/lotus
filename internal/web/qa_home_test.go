package web

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/uyab/lotus/internal/repo"
)

func TestNav_ProjectsQAIntegrationsMembersActivityOnly(t *testing.T) {
	var keys []string
	for _, it := range buildNav("projects", 3).Items {
		keys = append(keys, it.Key+"="+it.Href)
	}
	want := "projects=/projects qa_tools=/qa integrations=/settings/connections members=/settings/members activity=/activity"
	if got := strings.Join(keys, " "); got != want {
		t.Fatalf("nav = %s\nwant  %s", got, want)
	}
}

// Members has its own sidebar entry, lit on its page (the settings tab
// stays too, for anyone arriving through Settings).
func TestNav_MembersPageLightsTheMembersItem(t *testing.T) {
	e := newQAEnv(t, "acme-nav-members")
	status, body := e.get(t, e.owner, "/settings/members")
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	if !strings.Contains(body, `class="lotus-nav-item is-active" href="/settings/members"`) && !strings.Contains(body, `href="/settings/members" class="lotus-nav-item is-active"`) {
		t.Fatalf("Members is not the active sidebar item:\n%s", body)
	}
}

func TestQAHome_ListsProjectsWithTheirCounts(t *testing.T) {
	e := newQAEnv(t, "acme-qahome")
	ctx := context.Background()
	portal := e.project(t, "Portal <b>v2</b>")
	e.project(t, "Billing")
	sec, _ := repo.CreateQASection(ctx, e.s, portal, "Auth", nil)
	for _, title := range []string{"Login", "Logout"} {
		if _, err := repo.CreateQACase(ctx, e.s, portal, repo.QACaseInput{SectionID: sec.ID, Title: title}, nil); err != nil {
			t.Fatal(err)
		}
	}

	status, body := e.get(t, e.owner, "/qa")
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	if !strings.Contains(body, `href="/projects/`+portal.String()+`/qa"`) ||
		!strings.Contains(body, "Portal &lt;b&gt;v2&lt;/b&gt;") || strings.Contains(body, "<b>v2</b>") {
		t.Fatalf("portal row missing or unescaped:\n%s", body)
	}
	if !strings.Contains(body, `data-qa-cases="2"`) || !strings.Contains(body, "Billing") {
		t.Fatalf("counts or second project missing:\n%s", body)
	}
	if !strings.Contains(body, `class="lotus-nav-item is-active" href="/qa"`) && !strings.Contains(body, `href="/qa" class="lotus-nav-item is-active"`) {
		t.Fatalf("QA Tools is not the active sidebar item:\n%s", body)
	}
}

func TestQAHome_GuestSeesOnlyItsProjects(t *testing.T) {
	e := newQAEnv(t, "acme-qahome-guest")
	ctx := context.Background()
	mine := e.project(t, "Client portal")
	e.project(t, "Internal billing")
	guest, guestID := e.session(t, "client@bank.example", "guest")
	if err := repo.AddProjectMember(ctx, e.s, mine, guestID); err != nil {
		t.Fatal(err)
	}

	status, body := e.get(t, guest, "/qa")
	if status != http.StatusOK || !strings.Contains(body, "Client portal") || strings.Contains(body, "Internal billing") {
		t.Fatalf("guest /qa = %d:\n%s", status, body)
	}

	// Removed from every project: an empty list, not every project.
	repo.RemoveProjectMember(ctx, e.s, mine, guestID)
	_, body = e.get(t, guest, "/qa")
	if strings.Contains(body, "Client portal") || strings.Contains(body, "Internal billing") {
		t.Fatalf("guest on no projects sees projects:\n%s", body)
	}
}
