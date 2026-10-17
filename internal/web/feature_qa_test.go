package web

import (
	"net/http"
	"strings"
	"testing"
)

// LOTUS_FEATURE_QA is off by default, so the off state is what every
// instance runs unless someone decides otherwise. Each test here checks
// the same surface both ways: off proves QA is gone, and on proves the
// assertion is aimed at something that really exists — an off-state check
// against a URL that 404s either way would pass and prove nothing.

func qaEnvWithFeature(t *testing.T, sub string, on bool) qaEnv {
	t.Helper()
	return newQAEnvWith(t, sub, func(d *Deps) { d.Cfg.Features.QA = on })
}

func TestQAFeature_OffMeansEveryQAURLIsNotFound(t *testing.T) {
	t.Parallel()
	// One per route group: the UI, a guest's home, the share/workload
	// pages, the JSON API and the MCP server are registered separately,
	// so each could be left behind on its own.
	paths := []string{"/qa", "/qa/my", "/qa/workload", "/api/qa/projects", "/mcp/qa"}

	off := qaEnvWithFeature(t, "qa-flag-off", false)
	on := qaEnvWithFeature(t, "qa-flag-on", true)

	for _, path := range paths {
		if status, _ := off.get(t, off.owner, path); status != http.StatusNotFound {
			t.Errorf("QA off: GET %s = %d, want 404", path, status)
		}
		if status, _ := on.get(t, on.owner, path); status == http.StatusNotFound {
			t.Errorf("QA on: GET %s = 404 — the off-state assertion for it proves nothing", path)
		}
	}
}

func TestQAFeature_OffRemovesTheSidebarEntry(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		on   bool
		want bool
	}{{false, false}, {true, true}} {
		e := qaEnvWithFeature(t, "qa-nav", tc.on)
		status, body := e.get(t, e.owner, "/projects")
		if status != http.StatusOK {
			t.Fatalf("QA %v: /projects = %d", tc.on, status)
		}
		if got := strings.Contains(body, `href="/qa"`); got != tc.want {
			t.Errorf("QA %v: sidebar links to /qa = %v, want %v", tc.on, got, tc.want)
		}
	}
}

func TestQAFeature_OffRemovesQAFromTheDashboard(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		on   bool
		want bool
	}{{false, false}, {true, true}} {
		e := qaEnvWithFeature(t, "qa-dash", tc.on)
		status, body := e.get(t, e.owner, "/dashboard")
		if status != http.StatusOK {
			t.Fatalf("QA %v: /dashboard = %d", tc.on, status)
		}
		if got := strings.Contains(body, "/qa/workload"); got != tc.want {
			t.Errorf("QA %v: dashboard links to the QA workload = %v, want %v", tc.on, got, tc.want)
		}
	}
}

func TestQAFeature_OffRemovesTheProjectsTestCasesButton(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		on   bool
		want bool
	}{{false, false}, {true, true}} {
		e := qaEnvWithFeature(t, "qa-proj", tc.on)
		e.project(t, "Portal")
		status, body := e.get(t, e.owner, "/projects/Portal")
		if status != http.StatusOK {
			t.Fatalf("QA %v: project page = %d", tc.on, status)
		}
		if got := strings.Contains(body, "Test cases"); got != tc.want {
			t.Errorf("QA %v: project page offers Test cases = %v, want %v", tc.on, got, tc.want)
		}
	}
}

// The tokens page hands out a copy-paste MCP config. With QA off that
// config would point an assistant at a 404.
func TestQAFeature_OffRemovesTheQAAPISectionFromTokens(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		on   bool
		want bool
	}{{false, false}, {true, true}} {
		e := qaEnvWithFeature(t, "qa-tok", tc.on)
		status, body := e.get(t, e.owner, "/settings/tokens")
		if status != http.StatusOK {
			t.Fatalf("QA %v: tokens page = %d", tc.on, status)
		}
		if got := strings.Contains(body, "/mcp/qa"); got != tc.want {
			t.Errorf("QA %v: tokens page shows the QA MCP endpoint = %v, want %v", tc.on, got, tc.want)
		}
	}
}
