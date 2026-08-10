package sentry

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestProjectParsesStringIDsAndTeams(t *testing.T) {
	// Sentry returns numeric ids as JSON strings, and newer versions dropped the
	// singular `team` field in favour of `teams`.
	cases := map[string]string{
		"legacy team field": `{"id":"209","slug":"alurkerja-simulation-be","name":"Simulation BE",
			"platform":"go-fiber","team":{"id":"57","slug":"alurkerja-simulation"},
			"organization":{"slug":"javan"}}`,
		"teams array": `{"id":"209","slug":"alurkerja-simulation-be","name":"Simulation BE",
			"platform":"go-fiber","teams":[{"id":"57","slug":"alurkerja-simulation"}],
			"organization":{"slug":"javan"}}`,
	}

	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if got, want := r.URL.Path, "/api/0/projects/javan/alurkerja-simulation-be/"; got != want {
					t.Errorf("path = %q, want %q", got, want)
				}
				if got := r.Header.Get("Authorization"); got != "Bearer tok" {
					t.Errorf("Authorization = %q, want Bearer tok", got)
				}
				fmt.Fprint(w, body)
			}))
			defer srv.Close()

			p, err := New(srv.URL, "tok", "javan", srv.Client()).Project(context.Background(), "alurkerja-simulation-be")
			if err != nil {
				t.Fatalf("Project: %v", err)
			}
			if p.ID != 209 {
				t.Errorf("ID = %d, want 209", p.ID)
			}
			if p.TeamID != 57 || p.TeamSlug != "alurkerja-simulation" {
				t.Errorf("team = %d/%q, want 57/alurkerja-simulation", p.TeamID, p.TeamSlug)
			}
			if p.Platform != "go-fiber" || p.Org != "javan" {
				t.Errorf("platform/org = %q/%q", p.Platform, p.Org)
			}
		})
	}
}

func TestEnvironmentsSkipsHidden(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[{"name":"production","isHidden":false},
			{"name":"old-staging","isHidden":true},
			{"name":"","isHidden":false},
			{"name":"docker-JAVAN","isHidden":false}]`)
	}))
	defer srv.Close()

	envs, err := New(srv.URL, "tok", "javan", srv.Client()).Environments(context.Background(), "p")
	if err != nil {
		t.Fatalf("Environments: %v", err)
	}
	want := []string{"production", "docker-JAVAN"}
	if len(envs) != len(want) {
		t.Fatalf("envs = %v, want %v", envs, want)
	}
	for i := range want {
		if envs[i] != want[i] {
			t.Errorf("envs[%d] = %q, want %q", i, envs[i], want[i])
		}
	}
}

func TestIssuesFollowsCursorPaginationAndParsesCount(t *testing.T) {
	var seenCursors []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		seenCursors = append(seenCursors, q.Get("cursor"))
		if got := q.Get("query"); got != "is:unresolved" {
			t.Errorf("query = %q", got)
		}
		if got := q.Get("environment"); got != "production" {
			t.Errorf("environment = %q", got)
		}

		if q.Get("cursor") == "" {
			// First page advertises another page.
			w.Header().Set("Link", `<https://x/?cursor=prev>; rel="previous"; results="false"; cursor="prev", `+
				`<https://x/?cursor=c2>; rel="next"; results="true"; cursor="c2"`)
			fmt.Fprint(w, `[{"id":"1","shortId":"A-1","title":"boom","status":"unresolved",
				"substatus":"escalating","count":"3414","permalink":"https://s/1"}]`)
			return
		}
		// Last page: results="false" ends the walk.
		w.Header().Set("Link", `<https://x/?cursor=c3>; rel="next"; results="false"; cursor="c3"`)
		fmt.Fprint(w, `[{"id":"2","shortId":"A-2","title":"thud","status":"unresolved",
			"substatus":"new","count":"7"}]`)
	}))
	defer srv.Close()

	issues, err := New(srv.URL, "tok", "javan", srv.Client()).Issues(context.Background(), IssueQuery{
		ProjectSlug: "p",
		Environment: "production",
		Query:       "is:unresolved",
	})
	if err != nil {
		t.Fatalf("Issues: %v", err)
	}
	if len(issues) != 2 {
		t.Fatalf("got %d issues, want 2", len(issues))
	}
	if issues[0].Count != 3414 {
		t.Errorf("count = %d, want 3414 (string→int)", issues[0].Count)
	}
	if issues[0].Substatus != "escalating" || issues[1].Substatus != "new" {
		t.Errorf("substatus = %q/%q", issues[0].Substatus, issues[1].Substatus)
	}
	if len(seenCursors) != 2 || seenCursors[0] != "" || seenCursors[1] != "c2" {
		t.Errorf("cursors = %v, want [\"\" \"c2\"]", seenCursors)
	}
}

func TestIssuesStopsAtLimit(t *testing.T) {
	pages := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pages++
		w.Header().Set("Link", `<https://x/>; rel="next"; results="true"; cursor="more"`)
		fmt.Fprint(w, `[{"id":"1","count":"1"},{"id":"2","count":"1"},{"id":"3","count":"1"}]`)
	}))
	defer srv.Close()

	issues, err := New(srv.URL, "tok", "javan", srv.Client()).Issues(context.Background(), IssueQuery{ProjectSlug: "p", Limit: 2})
	if err != nil {
		t.Fatalf("Issues: %v", err)
	}
	if len(issues) != 2 {
		t.Fatalf("got %d issues, want limit of 2", len(issues))
	}
	if pages != 1 {
		t.Errorf("requested %d pages, want 1 — limit should stop the walk", pages)
	}
}

func TestIssuesSurfacesHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"detail":"You do not have permission"}`)
	}))
	defer srv.Close()

	_, err := New(srv.URL, "tok", "javan", srv.Client()).Issues(context.Background(), IssueQuery{ProjectSlug: "p"})
	if err == nil {
		t.Fatal("want error on 403, got nil")
	}
	if got := err.Error(); !contains(got, "403") || !contains(got, "permission") {
		t.Errorf("error = %q, want it to carry status + body", got)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestTagFilterQueryQuotesTheValue(t *testing.T) {
	cases := []struct {
		name, base, key, value, want string
	}{
		{"appends to an existing search", "is:unresolved", "service_name", "bpm-service",
			`is:unresolved service_name:"bpm-service"`},
		{"stands alone when there is no base", "", "service_name", "bpm-service",
			`service_name:"bpm-service"`},
		{"quoting survives a space", "is:unresolved", "service_name", "my service",
			`is:unresolved service_name:"my service"`},
		{"quoting survives a colon", "is:unresolved", "service_name", "a:b",
			`is:unresolved service_name:"a:b"`},
		{"embedded quotes are escaped", "is:unresolved", "service_name", `a"b`,
			`is:unresolved service_name:"a\"b"`},
		{"empty value leaves the search untouched", "is:unresolved", "service_name", "",
			"is:unresolved"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := TagFilterQuery(c.base, c.key, c.value); got != c.want {
				t.Errorf("TagFilterQuery = %q, want %q", got, c.want)
			}
		})
	}
}

func TestTagValuesTreatsUnknownTagAsEmpty(t *testing.T) {
	// Sentry only creates a tag once an event carries it, so a 404 means
	// "nobody is tagging yet" — a normal state, not a failure.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"detail":"The requested resource does not exist"}`)
	}))
	defer srv.Close()

	values, err := New(srv.URL, "tok", "javan", srv.Client()).TagValues(context.Background(), "p", "service_name")
	if err != nil {
		t.Fatalf("want nil error on 404, got %v", err)
	}
	if len(values) != 0 {
		t.Errorf("values = %v, want empty", values)
	}
}

func TestTagValuesListsSeenValues(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.Path, "/api/0/projects/javan/alurkerja-javan-be/tags/service_name/values/"; got != want {
			t.Errorf("path = %q, want %q", got, want)
		}
		fmt.Fprint(w, `[{"value":"bpm-service"},{"value":"  "},{"value":"report-service"}]`)
	}))
	defer srv.Close()

	values, err := New(srv.URL, "tok", "javan", srv.Client()).TagValues(context.Background(), "alurkerja-javan-be", "service_name")
	if err != nil {
		t.Fatalf("TagValues: %v", err)
	}
	if len(values) != 2 || values[0] != "bpm-service" || values[1] != "report-service" {
		t.Errorf("values = %v, want the two non-blank entries", values)
	}
}

func TestPingSucceedsOnValidOrgAndToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.Path, "/api/0/organizations/javan/"; got != want {
			t.Errorf("path = %q, want %q", got, want)
		}
		fmt.Fprint(w, `{"slug":"javan"}`)
	}))
	defer srv.Close()

	if err := New(srv.URL, "tok", "javan", srv.Client()).Ping(context.Background()); err != nil {
		t.Fatalf("Ping: %v", err)
	}
}

func TestListProjectsPaginatesLikeIssues(t *testing.T) {
	page := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.Path, "/api/0/organizations/javan/projects/"; got != want {
			t.Errorf("path = %q, want %q", got, want)
		}
		page++
		if page == 1 {
			w.Header().Set("Link", `<https://x/>; rel="next"; results="true"; cursor="c2"`)
			fmt.Fprint(w, `[{"slug":"a","name":"A"}]`)
			return
		}
		w.Header().Set("Link", `<https://x/>; rel="next"; results="false"; cursor="c3"`)
		fmt.Fprint(w, `[{"slug":"b","name":"B"}]`)
	}))
	defer srv.Close()

	projects, err := New(srv.URL, "tok", "javan", srv.Client()).ListProjects(context.Background())
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if len(projects) != 2 || projects[0].Slug != "a" || projects[1].Slug != "b" {
		t.Fatalf("projects = %+v, want [a b]", projects)
	}
}

func TestStatusErrorCarriesRetryAfterOn429(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, `{"detail":"rate limited"}`)
	}))
	defer srv.Close()

	_, err := New(srv.URL, "tok", "javan", srv.Client()).Project(context.Background(), "p")
	var se *StatusError
	if !as(err, &se) {
		t.Fatalf("err = %v, want *StatusError", err)
	}
	if se.StatusCode != http.StatusTooManyRequests {
		t.Errorf("StatusCode = %d, want 429", se.StatusCode)
	}
	if se.RetryAfter.Seconds() != 30 {
		t.Errorf("RetryAfter = %v, want 30s", se.RetryAfter)
	}
}

func as(err error, target **StatusError) bool {
	se, ok := err.(*StatusError)
	if !ok {
		return false
	}
	*target = se
	return true
}
