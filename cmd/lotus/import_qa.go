package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/uyab/lotus/internal/blob"
	"github.com/uyab/lotus/internal/config"
	"github.com/uyab/lotus/internal/db"
	"github.com/uyab/lotus/internal/qaimport"
	"github.com/uyab/lotus/internal/repo/global"
	"github.com/uyab/lotus/internal/repo/legacyqa"
)

func runImportQA(cfg config.Config, args []string) {
	verify := len(args) > 0 && args[0] == "verify"
	if verify {
		args = args[1:]
	}
	fs := flag.NewFlagSet("import-qa", flag.ExitOnError)
	legacyDSN := fs.String("legacy-dsn", "", "read-only DSN of the legacy QA Tools database (required)")
	orgSlug := fs.String("org", "", "subdomain of the Lotus organization to import into (required)")
	usersMap := fs.String("users-map", "", "CSV legacy_email,canonical_email")
	projectsMap := fs.String("projects-map", "", "CSV legacy_project_id,lotus_project_slug")
	folder := fs.String("folder", "QA Tools (imported)", "folder for newly created projects (empty = root)")
	dormant := fs.String("dormant-before", time.Now().AddDate(-1, 0, 0).Format("2006-01-02"), "no activity since then = no membership")
	closeIdle := fs.String("close-idle-before", time.Now().AddDate(0, 0, -30).Format("2006-01-02"), "close plans idle since before this date (empty = none)")
	filesDir := fs.String("files-dir", "", "mirror of the legacy MinIO bucket (empty = skip attachments)")
	dryRun := fs.Bool("dry-run", false, "run everything, then roll back")
	reportPath := fs.String("report", "", "write the JSON report here")
	fs.Parse(args)
	if *legacyDSN == "" || *orgSlug == "" {
		fs.Usage()
		os.Exit(2)
	}

	ctx := context.Background()
	appDB, err := db.OpenApp(cfg.DatabaseURL)
	if err != nil {
		log.Fatal(err)
	}
	defer appDB.Close()
	org, err := global.OrgBySubdomain(ctx, appDB, *orgSlug)
	if err != nil || org == nil {
		log.Fatalf("organization %q: %v", *orgSlug, err)
	}
	legacy, err := legacyqa.Open(*legacyDSN)
	if err != nil {
		log.Fatal(err)
	}
	defer legacy.Close()

	if verify {
		mm, err := qaimport.Verify(ctx, appDB, legacy, org.ID)
		if err != nil {
			log.Fatal(err)
		}
		for _, m := range mm {
			fmt.Printf("%-50s %-20s legacy=%-6d lotus=%d\n", m.Project, m.Field, m.Legacy, m.Lotus)
		}
		if len(mm) > 0 {
			fmt.Printf("%d mismatches\n", len(mm))
			os.Exit(1)
		}
		fmt.Println("verify: ok")
		return
	}

	opt := qaimport.Options{OrgID: org.ID, FolderName: *folder, FilesDir: *filesDir, DryRun: *dryRun}
	if opt.DormantBefore, err = time.Parse("2006-01-02", *dormant); err != nil {
		log.Fatalf("--dormant-before: %v", err)
	}
	if *closeIdle != "" {
		if opt.CloseIdleBefore, err = time.Parse("2006-01-02", *closeIdle); err != nil {
			log.Fatalf("--close-idle-before: %v", err)
		}
	}
	if *usersMap != "" {
		if opt.UsersMap, err = qaimport.LoadUsersMap(*usersMap); err != nil {
			log.Fatal(err)
		}
	}
	if *projectsMap != "" {
		if opt.ProjectsMap, err = qaimport.LoadProjectsMap(*projectsMap); err != nil {
			log.Fatal(err)
		}
	}
	store, err := blob.NewFS(cfg.BlobDir)
	if err != nil {
		log.Fatal(err)
	}
	rep, runErr := qaimport.New(appDB, legacy, store, opt).Run(ctx)
	out, _ := json.MarshalIndent(rep, "", "  ")
	if *reportPath != "" {
		if err := os.WriteFile(*reportPath, out, 0o644); err != nil {
			log.Printf("write report: %v", err)
		}
	}
	fmt.Println(string(out))
	if runErr != nil {
		log.Fatalf("import stopped: %v (re-run to resume; finished projects are skipped)", runErr)
	}
}
