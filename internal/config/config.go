// Package config loads Lotus's runtime configuration from the environment.
package config

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	Addr        string
	DatabaseURL string
	// MigrateDatabaseURL is the DSN used only by `lotus migrate`, connecting
	// as the lotus_migrate role (has DDL). DatabaseURL connects as lotus_app
	// (no DDL, subject to RLS). Falls back to DatabaseURL when unset, which is
	// fine for local dev where one role is fine.
	MigrateDatabaseURL string
	Dev                bool
	SessionSecret      string
	CookieSecure       bool

	// BaseDomain is the wildcard root Lotus is served under: apex is
	// BaseDomain, auth host is "auth."+BaseDomain, tenants are
	// "<slug>."+BaseDomain. No production value has been chosen yet
	// (tracked as a decision in ActiveCollab project 697) — the default
	// below is dev-only.
	BaseDomain string

	// KEK is the base64-encoded key-encryption-key for envelope-encrypting
	// per-organization integration credentials (task 2.2). Required in
	// production; a fixed dev-only value is used when unset so local runs
	// don't need it set before Phase 2 exists.
	KEK string

	WorkerCount int
}

func (c Config) AuthHost() string   { return "auth." + c.BaseDomain }
func (c Config) TenantHost(slug string) string { return slug + "." + c.BaseDomain }

// Load builds the config from environment variables.
//
// In production the process is configured entirely via real environment
// variables (systemd EnvironmentFile, rendered from GitLab CI/CD variables),
// where LOTUS_DEV=0 is set — so no .env file is ever read on the server. The
// .env load below is a local-dev convenience only, skipped whenever
// LOTUS_DEV=0 is already present in the environment.
func Load() Config {
	if os.Getenv("LOTUS_DEV") != "0" {
		loadDotEnv(".env")
	}
	c := Config{
		Addr:               envOr("ADDR", ":8080"),
		DatabaseURL:        envOr("DATABASE_URL", "postgres://postgres@localhost:5418/lotus?sslmode=disable"),
		MigrateDatabaseURL: os.Getenv("MIGRATE_DATABASE_URL"),
		Dev:                os.Getenv("LOTUS_DEV") == "1",
		SessionSecret:      os.Getenv("SESSION_SECRET"),
		BaseDomain:         envOr("LOTUS_BASE_DOMAIN", "lotus.localhost"),
		KEK:                os.Getenv("LOTUS_KEK"),
		WorkerCount:        envInt("LOTUS_WORKER_COUNT", 4),
	}
	if c.MigrateDatabaseURL == "" {
		c.MigrateDatabaseURL = c.DatabaseURL
	}
	if c.SessionSecret == "" {
		b := make([]byte, 32)
		rand.Read(b)
		c.SessionSecret = hex.EncodeToString(b)
	}
	// Session cookies are Secure (HTTPS-only) in production; defaults off in
	// dev (plain-HTTP localhost). Override with COOKIE_SECURE=1/0.
	c.CookieSecure = !c.Dev
	if v := os.Getenv("COOKIE_SECURE"); v != "" {
		c.CookieSecure = v == "1" || v == "true"
	}
	return c
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envInt(k string, def int) int {
	v := os.Getenv(k)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

func loadDotEnv(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if _, exists := os.LookupEnv(k); !exists {
			os.Setenv(k, v)
		}
	}
}
