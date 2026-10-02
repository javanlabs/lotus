package web

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gofiber/fiber/v2"

	"github.com/uyab/lotus/internal/httpx"
)

// A browser that navigates to a page Lotus refuses used to get whatever the
// refusing layer happened to write: Fiber's plain-text "Cannot GET …", or
// writeRepoErr's {"error": "not found"} JSON. Both read as a broken app.
// Browser navigations now get a page that says which of four things went
// wrong (sign-in, access, missing, server); every other client — htmx
// fragments, the JSON API, MCP, curl — keeps the response it got before.

type errorPageData struct {
	Status    int
	Heading   string
	Body      string
	LoginHref string
}

// wantsErrorPage reports whether c is a browser navigation, the only
// request an HTML error page is meant for. htmx swaps handle their own
// errors, and /api and /mcp clients parse JSON.
func wantsErrorPage(c *fiber.Ctx) bool {
	if c.Get("HX-Request") != "" {
		return false
	}
	p := c.Path()
	if strings.HasPrefix(p, "/api/") || strings.HasPrefix(p, "/mcp/") || strings.HasPrefix(p, "/static/") {
		return false
	}
	return strings.Contains(c.Get(fiber.HeaderAccept), "text/html")
}

func errorPageFor(status int, single bool) errorPageData {
	d := errorPageData{Status: status}
	switch {
	case status == fiber.StatusUnauthorized:
		d.Heading = "Sign in to continue"
		d.Body = "Your session has ended or you are not signed in to this organization."
		d.LoginHref = "/handoff/start"
		if single {
			d.LoginHref = "/login"
		}
	case status == fiber.StatusForbidden:
		d.Heading = "You don't have access to this page"
		d.Body = "Your role in this organization doesn't allow it. Ask an admin if you need access."
	case status == fiber.StatusNotFound:
		d.Heading = "Page not found"
		d.Body = "It may have been moved or deleted, or the link is wrong."
	case status >= 500:
		d.Heading = "Something went wrong"
		d.Body = "Lotus couldn't finish this request. Try again in a moment; if it keeps happening, tell your admin."
	default:
		d.Heading = http.StatusText(status)
		d.Body = "Lotus couldn't handle this request."
	}
	return d
}

func renderErrorPage(c *fiber.Ctx, r *httpx.Renderer, status int, single bool) error {
	data := errorPageFor(status, single)
	c.Response().ResetBody()
	c.Status(status).Type("html")
	return r.Render(c, "pages/error.html", httpx.ViewData{Title: data.Heading, Data: data})
}

// errorHandler is the app's fiber.Config.ErrorHandler: a returned error
// (fiber.NewError, an unmatched route, a recovered panic) becomes the error
// page for a browser navigation and Fiber's default response otherwise.
func errorHandler(d Deps) fiber.ErrorHandler {
	return func(c *fiber.Ctx, err error) error {
		status := fiber.StatusInternalServerError
		var fe *fiber.Error
		if errors.As(err, &fe) {
			status = fe.Code
		}
		if d.Renderer == nil || !wantsErrorPage(c) {
			return fiber.DefaultErrorHandler(c, err)
		}
		if rerr := renderErrorPage(c, d.Renderer, status, d.Cfg.IsSingle()); rerr != nil {
			return fiber.DefaultErrorHandler(c, err)
		}
		return nil
	}
}

// errorPages covers the other half: handlers that answer an error status
// themselves, without returning an error — writeRepoErr's JSON on an HTML
// route is the common case. A response that is already HTML (a form
// re-rendered with its validation message) is left alone.
func errorPages(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if err := c.Next(); err != nil {
			return err
		}
		status := c.Response().StatusCode()
		if status < 400 || d.Renderer == nil || !wantsErrorPage(c) {
			return nil
		}
		if strings.HasPrefix(string(c.Response().Header.ContentType()), "text/html") {
			return nil
		}
		return renderErrorPage(c, d.Renderer, status, d.Cfg.IsSingle())
	}
}
