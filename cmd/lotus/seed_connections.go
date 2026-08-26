package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/uyab/lotus/internal/config"
	"github.com/uyab/lotus/internal/crypto"
	"github.com/uyab/lotus/internal/db"
	"github.com/uyab/lotus/internal/repo"
	"github.com/uyab/lotus/internal/repo/global"
	"github.com/uyab/lotus/internal/tenant"
)

// connSeed is one integration this command can wire up. Secrets arrive
// through the environment rather than flags on purpose: a flag lands in
// shell history and is visible in `ps` output to every user on the box,
// while an env var read by one process is neither.
type connSeed struct {
	provider   string // integration_connections.provider
	name       string
	baseURLEnv string
	baseURLDef string
	// secret is assembled from these env vars; a "user:pass" pair for
	// Harbor, a single token for the rest.
	tokenEnv string
	userEnv  string
	// configEnv carries a non-secret connection setting the collector
	// needs beside the credential — Sentry's organization slug.
	configEnv, configKey, configDef string
}

var connSeeds = []connSeed{
	{provider: "gitlab", name: "GitLab", baseURLEnv: "GITLAB_BASE_URL",
		baseURLDef: "https://gitlab.javan.co.id", tokenEnv: "GITLAB_TOKEN"},
	{provider: "sonarqube", name: "SonarQube", baseURLEnv: "SONAR_BASE_URL",
		baseURLDef: "https://sonar.javan.co.id", tokenEnv: "SONAR_TOKEN"},
	{provider: "harbor_trivy", name: "Harbor", baseURLEnv: "HARBOR_BASE_URL",
		baseURLDef: "https://harbor.merapi.javan.id",
		tokenEnv:   "HARBOR_PASSWORD", userEnv: "HARBOR_USERNAME"},
	{provider: "sentry", name: "Sentry", baseURLEnv: "SENTRY_BASE_URL",
		baseURLDef: "https://sentry.cloud.javan.co.id", tokenEnv: "SENTRY_TOKEN",
		configEnv: "SENTRY_ORG", configKey: "org", configDef: "javan"},
}

// runSeedConnections points the organization's integration connections at
// real upstreams using credentials from the environment. Every secret goes
// through repo.CreateConnection / UpdateConnectionBaseURLWithSecret, so it
// is sealed under the org's DEK exactly as the settings UI would seal it —
// never written to the table directly, which is what produced the
// unopenable rows that once crashed the sync scheduler.
//
// Nothing here ever prints a secret. Providers with no credentials set in
// the environment are skipped, so partial setups work.
func runSeedConnections(cfg config.Config) {
	if !cfg.Dev {
		log.Fatal("lotus seed-connections: refusing to run without LOTUS_DEV=1 — use Settings › Connections in a real deployment")
	}

	kek, err := crypto.LoadKEK(cfg.KEK, 1)
	if err != nil {
		log.Fatalf("lotus seed-connections: KEK: %v", err)
	}
	conn, err := db.OpenApp(cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("lotus seed-connections: connect: %v", err)
	}
	defer conn.Close()
	ctx := context.Background()

	org, err := global.TheOnlyOrg(ctx, conn)
	if err != nil {
		log.Fatalf("lotus seed-connections: %v (run `lotus init` first)", err)
	}
	dek, err := global.GetOrCreateOrgDEK(ctx, conn, kek, org.ID)
	if err != nil {
		log.Fatalf("lotus seed-connections: org DEK: %v", err)
	}
	scope := tenant.FromRun(conn, org.ID)

	existing, err := repo.ListConnections(ctx, scope)
	if err != nil {
		log.Fatalf("lotus seed-connections: list: %v", err)
	}
	byProvider := map[string]*repo.Connection{}
	for i := range existing {
		if _, seen := byProvider[existing[i].Provider]; !seen {
			byProvider[existing[i].Provider] = &existing[i]
		}
	}

	wired, skipped := 0, []string{}
	for _, s := range connSeeds {
		secret := buildSecret(s)
		if secret == "" {
			skipped = append(skipped, s.provider)
			continue
		}
		baseURL := envOrDefault(s.baseURLEnv, s.baseURLDef)
		cfgJSON := json.RawMessage(`{}`)
		if s.configKey != "" {
			cfgJSON = json.RawMessage(fmt.Sprintf(`{%q:%q}`, s.configKey, envOrDefault(s.configEnv, s.configDef)))
		}

		if cur, ok := byProvider[s.provider]; ok {
			if _, err := repo.UpdateConnectionBaseURLWithSecret(ctx, scope, dek, cur.ID, baseURL, secret); err != nil {
				log.Fatalf("lotus seed-connections: update %s: %v", s.provider, err)
			}
			if s.configKey != "" {
				if _, err := repo.UpdateConnectionMeta(ctx, scope, cur.ID, cur.Name, cfgJSON); err != nil {
					log.Fatalf("lotus seed-connections: config %s: %v", s.provider, err)
				}
			}
			fmt.Printf("  %-13s updated  %s\n", s.provider, baseURL)
		} else {
			if _, err := repo.CreateConnection(ctx, scope, dek, repo.NewConnection{
				Provider: s.provider, Name: s.name, BaseURL: baseURL, Config: cfgJSON, Secret: secret,
			}); err != nil {
				log.Fatalf("lotus seed-connections: create %s: %v", s.provider, err)
			}
			fmt.Printf("  %-13s created  %s\n", s.provider, baseURL)
		}
		wired++
	}

	fmt.Printf("\n%d connection(s) wired for %q.\n", wired, org.Name)
	if len(skipped) > 0 {
		fmt.Printf("skipped (no credentials in the environment): %s\n", strings.Join(skipped, ", "))
	}
	fmt.Println("Secrets are sealed under the organization's DEK; none were printed or logged.")
}

// buildSecret returns "" when the credential this integration needs is not
// in the environment, which is the signal to skip it.
func buildSecret(s connSeed) string {
	token := os.Getenv(s.tokenEnv)
	if token == "" {
		return ""
	}
	if s.userEnv == "" {
		return token
	}
	// Harbor's collector splits the stored secret on the first colon.
	user := os.Getenv(s.userEnv)
	if user == "" {
		return ""
	}
	return user + ":" + token
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
