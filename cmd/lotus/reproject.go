package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"strconv"

	"github.com/uyab/lotus/internal/collector/trivy"
	"github.com/uyab/lotus/internal/config"
	"github.com/uyab/lotus/internal/db"
	"github.com/uyab/lotus/internal/repo"
	"github.com/uyab/lotus/internal/repo/global"
	"github.com/uyab/lotus/internal/reproject"
	"github.com/uyab/lotus/internal/tenant"
)

// runReproject recomputes every component's headlines and roll-ups from
// the snapshots already stored (internal/reproject) — after a change to
// how scores are computed, so every row switches over at once instead of
// at each binding's next sync. It contacts no vendor. --dry-run prints
// the before and after and writes nothing; it opens its connection with
// every transaction read-only, so not even a stray write can land.
func runReproject(cfg config.Config, args []string) {
	fs := flag.NewFlagSet("reproject", flag.ExitOnError)
	orgSlug := fs.String("org", "", "subdomain of the organization to reproject (default: the only one)")
	dryRun := fs.Bool("dry-run", false, "print the before and after, write nothing")
	quiet := fs.Bool("quiet", false, "print one line per project, not per component")
	if err := fs.Parse(args); err != nil {
		log.Fatal(err)
	}
	dsn := cfg.DatabaseURL
	if *dryRun {
		var err error
		if dsn, err = readOnlyDSN(dsn); err != nil {
			log.Fatalf("lotus reproject: %v", err)
		}
	}
	conn, err := db.OpenApp(dsn)
	if err != nil {
		log.Fatalf("lotus reproject: connect: %v", err)
	}
	defer conn.Close()

	ctx := context.Background()
	var org *global.Org
	if *orgSlug == "" {
		org, err = global.TheOnlyOrg(ctx, conn)
	} else {
		org, err = global.OrgBySubdomain(ctx, conn, *orgSlug)
	}
	if err != nil {
		log.Fatalf("lotus reproject: organization: %v", err)
	}
	settings, err := global.OrgSettings(ctx, conn, org.ID)
	if err != nil {
		log.Fatalf("lotus reproject: organization settings: %v", err)
	}
	scope := tenant.FromRun(conn, org.ID)
	plan, err := reproject.Build(ctx, scope, trivy.FailOnFromSettings(settings))
	if err != nil {
		log.Fatalf("lotus reproject: %v", err)
	}
	printPlan(os.Stdout, plan, *quiet)
	if *dryRun {
		log.Printf("lotus reproject: dry run, nothing written")
		return
	}
	n, err := reproject.Apply(ctx, scope, plan)
	if err != nil {
		log.Fatalf("lotus reproject: %v (rewrote %d component(s) before this error)", err, n)
	}
	log.Printf("lotus reproject: rewrote %d component(s)", n)
}

// readOnlyDSN adds default_transaction_read_only to a Postgres URL; pgx
// sends an unknown parameter to the server as a run-time setting.
func readOnlyDSN(dsn string) (string, error) {
	u, err := url.Parse(dsn)
	if err != nil {
		return "", fmt.Errorf("DATABASE_URL: %w", err)
	}
	q := u.Query()
	q.Set("default_transaction_read_only", "on")
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func printPlan(w io.Writer, plan reproject.Plan, quiet bool) {
	for _, p := range plan.Projects {
		fmt.Fprintf(w, "%s: %s %s → %s %s · %d of %d components need attention\n", p.Name,
			score(p.Before.Score), p.Before.Status, score(p.After.Score), p.After.Status, p.After.NeedAttention, p.After.Components)
		if quiet {
			continue
		}
		for _, c := range p.Components {
			fmt.Fprintf(w, "  %s: %s %s → %s %s\n", c.Name, score(c.BeforeScore), c.BeforeStatus, score(c.AfterScore), c.AfterStatus)
			for _, k := range c.Kinds {
				fmt.Fprintf(w, "    %-9s %s → %s\n", k.Kind, kindText(k.Before), kindText(k.After))
			}
		}
	}
}

func score(s *int) string {
	if s == nil {
		return "–"
	}
	return strconv.Itoa(*s)
}

func kindText(kh repo.KindHeadline) string {
	t := score(kh.Score) + " " + kh.Status
	if kh.Grade != "" {
		t += " (" + kh.Grade + ")"
	}
	if kh.StateLabel != "" {
		t += " · " + kh.StateLabel
	}
	return t
}
