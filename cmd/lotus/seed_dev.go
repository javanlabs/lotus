package main

import (
	"context"
	"fmt"
	"log"

	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/config"
	"github.com/uyab/lotus/internal/db"
	"github.com/uyab/lotus/internal/repo/global"
)

// devSeedUsers is one account per role, so every permission path in the
// RBAC matrix can be exercised by clicking rather than by editing rows.
var devSeedUsers = []struct{ Role, Name string }{
	{"owner", "Dev Owner"},
	{"admin", "Dev Admin"},
	{"member", "Dev Member"},
	{"viewer", "Dev Viewer"},
}

// runSeedDev creates the seeded development accounts the login page's
// one-click list draws from. Refuses to run outside LOTUS_DEV=1: these
// accounts exist to be impersonated without a password, which is exactly
// what must never be possible anywhere real.
func runSeedDev(cfg config.Config) {
	if !cfg.Dev {
		log.Fatal("lotus seed-dev: refusing to run without LOTUS_DEV=1 — these accounts can be logged into without a credential")
	}

	conn, err := db.Open(cfg.MigrateDatabaseURL)
	if err != nil {
		log.Fatalf("lotus seed-dev: connect: %v", err)
	}
	defer conn.Close()
	ctx := context.Background()

	org, err := global.TheOnlyOrg(ctx, conn)
	if err != nil {
		log.Fatalf("lotus seed-dev: %v (run `lotus init` first)", err)
	}

	for _, u := range devSeedUsers {
		email := u.Role + global.DevUserSuffix

		// Verified email on purpose: magic links only ever go to verified
		// addresses, and an unverifiable seed account could not log in.
		var userID uuid.UUID
		if err := conn.GetContext(ctx, &userID, `
			INSERT INTO users (email, name, email_verified_at) VALUES ($1, $2, now())
			ON CONFLICT (email) DO UPDATE SET name = EXCLUDED.name
			RETURNING id`, email, u.Name); err != nil {
			log.Fatalf("lotus seed-dev: create %s: %v", email, err)
		}

		if _, err := conn.ExecContext(ctx, `
			INSERT INTO memberships (org_id, user_id, role_id)
			SELECT $1, $2, r.id FROM roles r WHERE r.key = $3 AND r.org_id IS NULL
			ON CONFLICT (org_id, user_id) DO NOTHING`, org.ID, userID, u.Role); err != nil {
			log.Fatalf("lotus seed-dev: membership for %s: %v", email, err)
		}
		fmt.Printf("  %-8s %s\n", u.Role, email)
	}

	fmt.Printf("\nSeeded %d development accounts in %q.\n", len(devSeedUsers), org.Name)
	fmt.Printf("Open %s/login and use the Dev mode list to sign in as any of them.\n", cfg.BaseURL())
}
