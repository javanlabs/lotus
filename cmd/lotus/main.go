// Command lotus is Lotus's single binary: `serve` runs the web+worker
// process, `migrate` applies schema changes as the lotus_migrate role,
// `import-legacy` (task 5.1) brings the old Lotus installation in as the
// first organization.
package main

import (
	"context"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"os"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/authn"
	"github.com/uyab/lotus/internal/config"
	"github.com/uyab/lotus/internal/crypto"
	"github.com/uyab/lotus/internal/db"
	"github.com/uyab/lotus/internal/httpx"
	"github.com/uyab/lotus/internal/mailer"
	"github.com/uyab/lotus/internal/netguard"
	"github.com/uyab/lotus/internal/repo/global"
	"github.com/uyab/lotus/internal/retention"
	"github.com/uyab/lotus/internal/scheduler"
	"github.com/uyab/lotus/internal/tenant"
	"github.com/uyab/lotus/internal/timeseries"
	"github.com/uyab/lotus/internal/web"
	lotusweb "github.com/uyab/lotus/web"

	// Blank-imported for their init() registration into the collector
	// registry (task 3.1) — this is the entire wiring a collector package
	// needs; see internal/collector's own doc comment for the full
	// 5-step checklist a fifth integration follows.
	_ "github.com/uyab/lotus/internal/collector/gitlab"
	_ "github.com/uyab/lotus/internal/collector/sentry"
	_ "github.com/uyab/lotus/internal/collector/sonarqube"
	_ "github.com/uyab/lotus/internal/collector/trivy"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}

	cfg := config.Load()
	if err := cfg.Validate(); err != nil {
		log.Fatal(err)
	}

	switch os.Args[1] {
	case "serve":
		runServe(cfg)
	case "migrate":
		runMigrate(cfg, os.Args[2:])
	case "init":
		runInit(cfg, os.Args[2:])
	case "rewrap-keys":
		runRewrapKeys(cfg, os.Args[2:])
	case "import-legacy":
		log.Fatal("import-legacy is not implemented yet (task 5.1)")
	default:
		usage()
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: lotus <serve|migrate up|migrate down|init --org-name=<name> --owner-email=<email> [--slug=<slug>]|rewrap-keys --old-kek=<base64>|import-legacy>")
}

// runRewrapKeys is task 2.2's KEK rotation command: unwraps every
// organization's DEK under --old-kek and re-wraps it under the KEK
// currently in LOTUS_KEK, touching only organizations.dek_ciphertext/
// dek_nonce/dek_kek_version — no credential row is read or written, so
// every already-open connection keeps decrypting fine throughout ("tanpa
// downtime baca").
func runRewrapKeys(cfg config.Config, args []string) {
	fs := flag.NewFlagSet("rewrap-keys", flag.ExitOnError)
	oldKEKFlag := fs.String("old-kek", "", "base64-encoded KEK currently wrapping every org's DEK")
	if err := fs.Parse(args); err != nil {
		log.Fatalf("parse args: %v", err)
	}
	if *oldKEKFlag == "" {
		log.Fatal("usage: lotus rewrap-keys --old-kek=<base64>  (new KEK is read from LOTUS_KEK)")
	}

	oldKEK, err := crypto.LoadKEK(*oldKEKFlag, 0)
	if err != nil {
		log.Fatalf("load --old-kek: %v", err)
	}
	newKEK, err := crypto.LoadKEK(cfg.KEK, oldKEK.Version+1)
	if err != nil {
		log.Fatalf("load LOTUS_KEK: %v", err)
	}

	sqlDB, err := db.OpenApp(cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer sqlDB.Close()

	n, err := global.RewrapOrgDEKs(context.Background(), sqlDB, oldKEK, newKEK)
	if err != nil {
		log.Fatalf("rewrap-keys: %v (rewrapped %d organization(s) before this error)", err, n)
	}
	log.Printf("rewrap-keys: ok, rewrapped %d organization(s) to KEK version %d", n, newKEK.Version)
}

func runServe(cfg config.Config) {
	sqlDB, err := db.OpenApp(cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer sqlDB.Close()

	kek, err := crypto.LoadKEK(cfg.KEK, 1)
	if err != nil {
		log.Fatalf("load KEK: %v", err)
	}

	templatesFS, staticFS := assetFilesystems(cfg.Dev)
	renderer := httpx.NewRenderer(templatesFS, cfg.Dev)
	resolver := tenant.NewResolver(cfg, sqlDB)
	app := web.New(web.Deps{
		Renderer:       renderer,
		StaticFS:       staticFS,
		Resolver:       resolver,
		DB:             sqlDB,
		Cfg:            cfg,
		OAuthProviders: oauthProviders(cfg),
		CookieSecure:   cfg.CookieSecure,
		Mailer:         mailer.New(cfg.SMTPHost, cfg.SMTPPort, cfg.SMTPUser, cfg.SMTPPass, cfg.SMTPFrom),
		KEK:            kek,
	})

	go sweepExpiredSessions(sqlDB)

	// Task 3.2: enqueuer + reaper + worker pool. Safe to run in every
	// replica — the enqueuer no-ops in every process but the one that
	// wins the advisory lock, and the claim query's SKIP LOCKED makes
	// concurrent workers (in this process or another) safe by
	// construction. Runs until the process exits; no graceful-shutdown
	// signal wiring yet (systemd just SIGKILLs on redeploy today — a
	// run this kills mid-lease is exactly what the reaper exists for).
	go scheduler.Run(context.Background(), scheduler.Options{
		DB:          sqlDB,
		Execute:     scheduler.DefaultExecute(sqlDB, kek, netguard.Config{AllowHTTP: cfg.Dev}, scheduler.NewLimiters(2, 5)),
		WorkerCount: cfg.WorkerCount,
	})

	// Retention (task 3.6) needs DDL — CREATE TABLE ... PARTITION OF,
	// DROP TABLE on an aged-out partition — which lotus_app was
	// deliberately never granted (db.OpenApp's own doc comment: it's the
	// RLS-restricted role). Reuses the same MigrateDatabaseURL role
	// `lotus migrate` already runs schema changes as, rather than
	// widening lotus_app's own privileges just for this.
	retentionDB, err := db.Open(cfg.MigrateDatabaseURL)
	if err != nil {
		log.Fatalf("open retention db: %v", err)
	}
	defer retentionDB.Close()
	go retention.Run(context.Background(), retentionDB, retention.Options{})

	// Timeseries (task 3.7): rollup + partition management, same
	// DDL-capable connection and reasoning as retention above.
	timeseriesDB, err := db.Open(cfg.MigrateDatabaseURL)
	if err != nil {
		log.Fatalf("open timeseries db: %v", err)
	}
	defer timeseriesDB.Close()
	go timeseries.Run(context.Background(), timeseriesDB, timeseries.Options{})

	log.Printf("lotus serving on %s (dev=%v)", cfg.Addr, cfg.Dev)
	if err := app.Listen(cfg.Addr); err != nil {
		log.Fatalf("serve: %v", err)
	}
}

// sweepExpiredSessions deletes expired session rows every 10 minutes
// (task 1.3). A plain ticker rather than the scheduler's queue (task 3.2,
// not yet implemented) — this is a single unconditional DELETE with no
// per-tenant work to fan out, so it doesn't need that machinery.
func sweepExpiredSessions(sqlDB *sqlx.DB) {
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		n, err := global.SweepExpiredSessions(context.Background(), sqlDB)
		if err != nil {
			log.Printf("session sweep: %v", err)
			continue
		}
		if n > 0 {
			log.Printf("session sweep: removed %d expired session(s)", n)
		}
	}
}

// oauthProviders registers only the providers with a client secret
// configured, so local dev works with zero, one, or both providers set up.
func oauthProviders(cfg config.Config) authn.Registry {
	registry := authn.Registry{}
	authHostBase := cfg.BaseURL()
	if cfg.GitHubClientSecret != "" {
		registry["github"] = authn.NewGitHubProvider(cfg.GitHubClientID, cfg.GitHubClientSecret, authHostBase+"/login/github/callback")
	}
	if cfg.GoogleClientSecret != "" {
		registry["google"] = authn.NewGoogleProvider(cfg.GoogleClientID, cfg.GoogleClientSecret, authHostBase+"/login/google/callback")
	}
	return registry
}

// assetFilesystems returns the templates and static roots: disk-backed in
// dev (LOTUS_DEV=1) for hot reload, embedded (web.Templates/web.Static)
// otherwise.
func assetFilesystems(dev bool) (templates fs.FS, static fs.FS) {
	if dev {
		return os.DirFS("web/templates"), os.DirFS("web/static")
	}
	sub := func(embedded fs.FS, dir string) fs.FS {
		s, err := fs.Sub(embedded, dir)
		if err != nil {
			log.Fatalf("embed sub %s: %v", dir, err)
		}
		return s
	}
	return sub(lotusweb.Templates, "templates"), sub(lotusweb.Static, "static")
}

func runMigrate(cfg config.Config, args []string) {
	if len(args) != 1 || (args[0] != "up" && args[0] != "down") {
		fmt.Fprintln(os.Stderr, "usage: lotus migrate <up|down>")
		os.Exit(1)
	}
	var err error
	if args[0] == "up" {
		err = db.Migrate(cfg.MigrateDatabaseURL)
	} else {
		err = db.MigrateDown(cfg.MigrateDatabaseURL)
	}
	if err != nil {
		log.Fatalf("migrate %s: %v", args[0], err)
	}
	log.Printf("migrate %s: ok", args[0])
}
