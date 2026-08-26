package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"regexp"
	"strings"

	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/config"
	"github.com/uyab/lotus/internal/db"
	"github.com/uyab/lotus/internal/repo/global"
)

var nonSlugChars = regexp.MustCompile(`[^a-z0-9-]+`)

// runInit is the single-mode bootstrap command: it creates the one
// organization and parks the pending Owner's email, so the first verified
// matching login becomes Owner. Run once, before the first `serve`.
//
// Uses db.Open (the migrate role) rather than db.OpenApp: this runs before
// any organization exists, so there is no org context for RLS to scope to
// and the restricted app role would see — and write — nothing.
func runInit(cfg config.Config, args []string) {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	orgName := fs.String("org-name", "", "organization display name (required)")
	ownerEmail := fs.String("owner-email", "", "email address that becomes Owner on first login (required)")
	slug := fs.String("slug", "", "organization slug; defaults to a slugified --org-name")
	if err := fs.Parse(args); err != nil {
		log.Fatal(err)
	}
	if *orgName == "" || *ownerEmail == "" {
		fs.Usage()
		log.Fatal("lotus init: --org-name and --owner-email are both required")
	}
	if *slug == "" {
		*slug = slugify(*orgName)
	}
	if *slug == "" {
		log.Fatalf("lotus init: could not derive a slug from --org-name %q; pass --slug explicitly", *orgName)
	}

	conn, err := db.Open(cfg.MigrateDatabaseURL)
	if err != nil {
		log.Fatalf("lotus init: connect: %v", err)
	}
	defer conn.Close()
	ctx := context.Background()

	// An organization already present means this instance is initialized.
	// Checking first gives a clear message; instance_bootstrap's single-row
	// primary key is the backstop if two inits race.
	if _, err := global.TheOnlyOrg(ctx, conn); err == nil {
		log.Fatal("lotus init: this instance already has an organization; a single-mode install holds exactly one")
	} else if !errors.Is(err, global.ErrNoOrg) {
		log.Fatalf("lotus init: %v", err)
	}

	var orgID uuid.UUID
	if err := conn.GetContext(ctx, &orgID,
		`INSERT INTO organizations (subdomain, name) VALUES ($1, $2) RETURNING id`, *slug, *orgName); err != nil {
		log.Fatalf("lotus init: create organization: %v", err)
	}
	if err := global.CreateBootstrap(ctx, conn, orgID, *ownerEmail); err != nil {
		log.Fatalf("lotus init: %v", err)
	}

	fmt.Printf("Initialized %q (slug %q).\n", *orgName, *slug)
	fmt.Printf("Sign in as %s to claim ownership. No other account can sign in until then.\n", *ownerEmail)
}

func slugify(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	s = strings.ReplaceAll(s, " ", "-")
	s = nonSlugChars.ReplaceAllString(s, "")
	return strings.Trim(s, "-")
}
