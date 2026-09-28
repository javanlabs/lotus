package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/uyab/lotus/internal/config"
	"github.com/uyab/lotus/internal/db"
	"github.com/uyab/lotus/internal/legacylotus"
	"github.com/uyab/lotus/internal/repo/global"
)

// runImportLegacy imports the legacy Lotus's metric history (SonarQube,
// GitLab, Trivy snapshots) from an export made on its server by
// deploy/legacy-lotus-export.py. Run it after seed-connections and
// seed-services, so the connections and bindings exist; it creates the
// Trivy bindings itself. Safe to run again: what a binding already holds
// is skipped.
func runImportLegacy(cfg config.Config, args []string) {
	fs := flag.NewFlagSet("import-legacy", flag.ExitOnError)
	file := fs.String("file", "", "the export written by deploy/legacy-lotus-export.py (required)")
	orgSlug := fs.String("org", "", "subdomain of the organization to import into (default: the only one)")
	dryRun := fs.Bool("dry-run", false, "read and map every row, report, write nothing")
	if err := fs.Parse(args); err != nil {
		log.Fatal(err)
	}
	if *file == "" {
		fs.Usage()
		log.Fatal("lotus import-legacy: --file is required")
	}
	if cfg.MigrateDatabaseURL == "" {
		log.Fatal("lotus import-legacy: MIGRATE_DATABASE_URL is required (past months need partitions, and the charts a rollup)")
	}
	f, err := os.Open(*file)
	if err != nil {
		log.Fatalf("lotus import-legacy: %v", err)
	}
	defer f.Close()

	app, err := db.OpenApp(cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("lotus import-legacy: connect: %v", err)
	}
	defer app.Close()
	ddl, err := db.Open(cfg.MigrateDatabaseURL)
	if err != nil {
		log.Fatalf("lotus import-legacy: connect (migrate role): %v", err)
	}
	defer ddl.Close()

	ctx := context.Background()
	var org *global.Org
	if *orgSlug == "" {
		org, err = global.TheOnlyOrg(ctx, app)
	} else {
		org, err = global.OrgBySubdomain(ctx, app, *orgSlug)
	}
	if err != nil || org == nil {
		log.Fatalf("lotus import-legacy: organization: %v", err)
	}

	started := time.Now()
	rep, err := legacylotus.Import(ctx, app, ddl, org.ID, f, legacylotus.Options{
		DryRun: *dryRun,
		Progress: func(month time.Time, n int) {
			log.Printf("import-legacy: %s done, %d snapshots", month.Format("2006-01"), n)
		},
	})
	printLegacyReport(rep, *dryRun, time.Since(started))
	if err != nil {
		log.Fatalf("lotus import-legacy: %v (run it again to resume)", err)
	}
}

func printLegacyReport(rep legacylotus.Report, dry bool, took time.Duration) {
	verb := "imported"
	if dry {
		verb = "would import"
	}
	fmt.Printf("\nexport of %s, %s to %s\n", rep.ExportedAt.Format(time.RFC3339),
		rep.From.Format("2006-01-02"), rep.To.Format("2006-01-02"))
	fmt.Printf("%d rows read · %d %s · %d already imported · %d unusable · %s\n",
		rep.Read, rep.Imported, verb, rep.AlreadyImported, rep.Unusable, took.Round(time.Second))
	for label, list := range map[string][]string{
		"unknown services (no component has this legacy name)":        rep.UnknownServices,
		"unknown Harbor images (no component mapping)":                rep.UnknownImages,
		"missing bindings (run seed-services after seed-connections)": rep.MissingBindings,
	} {
		if len(list) > 0 {
			fmt.Printf("%s: %v\n", label, list)
		}
	}
}
