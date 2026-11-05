package web

import (
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/authz"
)

// Screen actions are form posts: a browser form cannot PATCH or DELETE.
// Each one answers a rejection the way rejectInviteForm does — htmx gets
// the message inside the still-open dialog, a plain post gets the status —
// and a success with a flash and redirectAfterMutation.

// formError answers a rejected form.
func formError(c *fiber.Ctx, d Deps, status int, msg string) error {
	if c.Get("HX-Request") == "" {
		return fiber.NewError(status, msg)
	}
	c.Type("html")
	return d.Renderer.RenderNamed(c, "form_error", msg)
}

// auditActor is the signed-in person an audit event is recorded against,
// nil for a principal with no user (an organization service token).
func auditActor(c *fiber.Ctx) *uuid.UUID {
	if p, ok := authz.CurrentPrincipal(c); ok && p.UserID != uuid.Nil {
		id := p.UserID
		return &id
	}
	return nil
}

func uuidString(id *uuid.UUID) string {
	if id == nil {
		return ""
	}
	return id.String()
}
