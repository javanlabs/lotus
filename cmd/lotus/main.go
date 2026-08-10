// Command lotus is Lotus's single binary: `serve` runs the web+worker
// process, `migrate` applies schema changes as the lotus_migrate role,
// `import-legacy` (task 5.1) brings the old Lotus installation in as the
// first organization.
package main

import (
	"fmt"
	"io/fs"
	"log"
	"os"

	"github.com/uyab/lotus/internal/config"
	"github.com/uyab/lotus/internal/db"
	"github.com/uyab/lotus/internal/httpx"
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
	app := web.New(renderer, staticFS, resolver)

	log.Printf("lotus serving on %s (dev=%v)", cfg.Addr, cfg.Dev)
	if err := app.Listen(cfg.Addr); err != nil {
		log.Fatalf("serve: %v", err)
	}
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
