// Command lotus is Lotus's single binary: `serve` runs the web+worker
// process, `migrate` applies schema changes as the lotus_migrate role,
// `import-legacy` (task 5.1) brings the old Lotus installation in as the
// first organization.
package main

import (
	"context"
	"fmt"
	"io/fs"
	"log"
	"os"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/authn"
	"github.com/uyab/lotus/internal/config"
	"github.com/uyab/lotus/internal/db"
	"github.com/uyab/lotus/internal/httpx"
	"github.com/uyab/lotus/internal/mailer"
	"github.com/uyab/lotus/internal/repo/global"
	"github.com/uyab/lotus/internal/tenant"
	"github.com/uyab/lotus/internal/web"
	lotusweb "github.com/uyab/lotus/web"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}

	cfg := config.Load()

	switch os.Args[1] {
	case "serve":
		runServe(cfg)
	case "migrate":
		runMigrate(cfg, os.Args[2:])
	case "import-legacy":
		log.Fatal("import-legacy is not implemented yet (task 5.1)")
	default:
		usage()
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: lotus <serve|migrate up|migrate down|import-legacy>")
}

func runServe(cfg config.Config) {
	sqlDB, err := db.Open(cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer sqlDB.Close()

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
	})

	go sweepExpiredSessions(sqlDB)

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
	authHostBase := cfg.Scheme() + "://" + cfg.AuthHost()
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
