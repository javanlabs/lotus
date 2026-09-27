package authz_test

import (
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/authz"
)

func projectAccessApp(role string, member bool) (*fiber.App, uuid.UUID) {
	projectID := uuid.New()
	app := fiber.New()
	app.Use(func(c *fiber.Ctx) error {
		authz.SetPrincipal(c, authz.Principal{Kind: authz.KindSession, UserID: uuid.New(), Role: role})
		return c.Next()
	})
	check := func(c *fiber.Ctx, pid, uid uuid.UUID) (bool, error) { return member && pid == projectID, nil }
	app.Get("/projects/:id/qa", authz.RequireProjectAccess(check, "id"), func(c *fiber.Ctx) error {
		return c.SendString("ok")
	})
	return app, projectID
}

func TestRequireProjectAccess(t *testing.T) {
	cases := []struct {
		role   string
		member bool
		other  bool
		want   int
	}{
		{"member", false, false, 200}, // members see every project
		{"viewer", false, true, 200},  // so do viewers
		{"guest", true, false, 200},   // guest on its own project
		{"guest", false, false, 404},  // guest not listed
		{"guest", true, true, 404},    // guest listed elsewhere, asking for another id
	}
	for _, tc := range cases {
		app, pid := projectAccessApp(tc.role, tc.member)
		target := pid
		if tc.other {
			target = uuid.New()
		}
		resp, err := app.Test(httptest.NewRequest("GET", "/projects/"+target.String()+"/qa", nil))
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != tc.want {
			t.Errorf("%s member=%v other=%v: status %d, want %d", tc.role, tc.member, tc.other, resp.StatusCode, tc.want)
		}
	}
	// A malformed id is a 404 too, never a 500.
	app, _ := projectAccessApp("guest", true)
	resp, _ := app.Test(httptest.NewRequest("GET", "/projects/not-a-uuid/qa", nil))
	if resp.StatusCode != 404 {
		t.Errorf("malformed id: status %d, want 404", resp.StatusCode)
	}
}
