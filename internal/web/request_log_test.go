package web

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
)

// A share link or invitation link in the request log is the secret itself:
// anyone who reads the log could open it. The log keeps the route, not the
// token.
func TestRequestLog_RedactsLinkTokens(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	app := fiber.New()
	app.Use(requestLogger(&out))
	app.Get("/*", func(c *fiber.Ctx) error { return c.SendStatus(http.StatusNotFound) })

	secret := strings.Repeat("ab", 32)
	for _, path := range []string{"/s/qa/" + secret, "/invite/" + secret, "/invite/" + secret + "/confirm", "/projects/x?token=" + secret} {
		if _, err := app.Test(httptest.NewRequest(http.MethodGet, path, nil), -1); err != nil {
			t.Fatal(err)
		}
	}
	logged := out.String()
	if strings.Contains(logged, secret) {
		t.Fatalf("the log holds a token:\n%s", logged)
	}
	for _, want := range []string{"/s/qa/[redacted]", "/invite/[redacted]", "/projects/x"} {
		if !strings.Contains(logged, want) {
			t.Errorf("log lacks %q:\n%s", want, logged)
		}
	}
}
