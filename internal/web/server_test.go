package web

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"

	"github.com/uyab/lotus/internal/config"
	"github.com/uyab/lotus/internal/dbtest"
	"github.com/uyab/lotus/internal/httpx"
	"github.com/uyab/lotus/internal/tenant"
)

func newTestApp(t *testing.T) *fiber.App {
	t.Helper()
	renderer := httpx.NewRenderer(os.DirFS("../../web/templates"), true)
	db := dbtest.Open(t)
	resolver := tenant.NewResolver(config.Config{BaseDomain: "lotus.test", Dev: true}, db)
	return New(renderer, os.DirFS("../../web/static"), resolver)
}

func TestHealthz(t *testing.T) {
	app := newTestApp(t)
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	var body struct {
		OK bool `json:"ok"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if !body.OK {
		t.Fatalf("body.ok = false, want true")
	}
}

func TestHome(t *testing.T) {
	app := newTestApp(t)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	for _, want := range []string{"Lotus", "Check /healthz"} {
		if !strings.Contains(string(got), want) {
			t.Fatalf("home page missing %q:\n%s", want, got)
		}
	}
}
