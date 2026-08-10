// Package web wires Lotus's HTTP routes. Task 0.1 only needs a health check
// and one rendered page; the rest of the route table (auth, tenant app,
// marketing) is layered on in Fase 1/4 as those pieces land. /healthz and
// the Fase-0 sample page are deliberately host-agnostic — they exist for
// local dev and uptime checks, not as part of the apex/auth/tenant
// taxonomy task 0.5 establishes for the real route table.
package web

import (
	"io/fs"
	"net/http"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/filesystem"
	"github.com/gofiber/fiber/v2/middleware/logger"
	"github.com/gofiber/fiber/v2/middleware/recover"

	"github.com/uyab/lotus/internal/httpx"
	"github.com/uyab/lotus/internal/tenant"
)

// New builds the Fiber app. staticFS is rooted at the static assets
// directory (embedded in production, os.DirFS in dev — see cmd/lotus).
// resolver runs on every request so tenant.OrgFromContext/FromContext are
// available to any handler added from Fase 1 onward.
func New(renderer *httpx.Renderer, staticFS fs.FS, resolver *tenant.Resolver) *fiber.App {
	app := fiber.New(fiber.Config{
		AppName: "lotus",
	})
	app.Use(recover.New())
	app.Use(logger.New())
	app.Use(resolver.Middleware())

	app.Use("/static", filesystem.New(filesystem.Config{Root: http.FS(staticFS)}))

	app.Get("/healthz", healthz)
	app.Get("/", home(renderer))

	return app
}

func healthz(c *fiber.Ctx) error {
	return c.JSON(fiber.Map{"ok": true})
}

func home(renderer *httpx.Renderer) fiber.Handler {
	return func(c *fiber.Ctx) error {
		c.Type("html")
		return renderer.Render(c, "pages/home.html", httpx.ViewData{Title: "Home"})
	}
}
