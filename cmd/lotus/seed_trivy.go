package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/collector/trivy"
	"github.com/uyab/lotus/internal/config"
	"github.com/uyab/lotus/internal/db"
	"github.com/uyab/lotus/internal/repo"
	"github.com/uyab/lotus/internal/repo/global"
	"github.com/uyab/lotus/internal/tenant"
)

// legacyTrivyExport is the shape of `lotus-web trivy-export` from the old
// deployment: one row per Harbor repository, aggregate counts only.
type legacyTrivyExport struct {
	Snapshots []struct {
		Repo           string     `json:"repo"`
		Image          string     `json:"image"`
		Digest         string     `json:"digest"`
		Tags           string     `json:"tags"`
		Severity       string     `json:"severity"`
		Total          int        `json:"total"`
		Fixable        int        `json:"fixable"`
		Critical       int        `json:"critical"`
		High           int        `json:"high"`
		Medium         int        `json:"medium"`
		Low            int        `json:"low"`
		Unknown        int        `json:"unknown"`
		Pulls          int        `json:"pulls"`
		ArtifactCount  int        `json:"artifact_count"`
		ImagePushedAt  *time.Time `json:"image_pushed_at"`
		ScanFinishedAt *time.Time `json:"scan_finished_at"`
	} `json:"snapshots"`
}

// trivyImageToComponent maps the legacy Harbor image stem (the repository
// name with its -dev / -release suffix removed) onto a component slug.
// The old naming scheme is consistent enough that this short table covers
// every AlurKerja image; anything not listed is reported and skipped
// rather than guessed at.
var trivyImageToComponent = map[string]string{
	"alurkerja-apps":          "app-react-v2",
	"alurkerja-auth":          "authentication-service",
	"alurkerja-bpm":           "bpm-service",
	"alurkerja-camunda":       "camunda",
	"alurkerja-compro-be":     "company-profile-service",
	"alurkerja-compro":        "company-profile-react",
	"alurkerja-generate-test": "generate-test-service",
	"alurkerja-integration":   "integration-service",
	"alurkerja-microsite":     "microsite",
	"alurkerja-migration":     "migration-service",
	"alurkerja-notification":  "notification-service",
	"alurkerja-proxy":         "proxy-service",
	"alurkerja-report":        "report-service",
	"alurkerja-simulation-be": "simulation-service",
	"alurkerja-simulation":    "simulation-react",
	"alurkerja-studio":        "studio-react",
	"alurkerja-tenant":        "tenant-management-service",
}

func imageStem(image string) string {
	for _, suffix := range []string{"-new-release", "-release", "-dev"} {
		if strings.HasSuffix(image, suffix) {
			return strings.TrimSuffix(image, suffix)
		}
	}
	return image
}

// runSeedTrivy imports a legacy Trivy export: one binding per Harbor
// repository (so a component with a -dev and a -release image gets two,
// exercising the one-repo-many-images shape the model is built for), each
// persisted through the collector's own Persist and facts writer.
func runSeedTrivy(cfg config.Config, args []string) {
	fs := flag.NewFlagSet("seed-trivy", flag.ExitOnError)
	path := fs.String("file", "", "path to the legacy trivy export JSON (required)")
	if err := fs.Parse(args); err != nil {
		log.Fatal(err)
	}
	if !cfg.Dev {
		log.Fatal("lotus seed-trivy: refusing to run without LOTUS_DEV=1 — this writes fixture data")
	}
	if *path == "" {
		fs.Usage()
		log.Fatal("lotus seed-trivy: --file is required")
	}
	raw, err := os.ReadFile(*path)
	if err != nil {
		log.Fatalf("lotus seed-trivy: read: %v", err)
	}
	var export legacyTrivyExport
	if err := json.Unmarshal(raw, &export); err != nil {
		log.Fatalf("lotus seed-trivy: parse: %v", err)
	}

	conn, err := db.OpenApp(cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("lotus seed-trivy: connect: %v", err)
	}
	defer conn.Close()
	ctx := context.Background()

	org, err := global.TheOnlyOrg(ctx, conn)
	if err != nil {
		log.Fatalf("lotus seed-trivy: %v (run `lotus init` first)", err)
	}
	scope := tenant.FromRun(conn, org.ID)

	// Every tenant-table read and write goes through scope.Tx: the app
	// role is RLS-restricted, and RLS only sees the organization once
	// SET LOCAL app.org_id has run inside the transaction. A bare query
	// on this connection silently returns nothing — which is the
	// guardrail doing its job, not a bug to work around with the migrate
	// role.
	var harborConnID uuid.UUID
	componentBySlug := map[string]uuid.UUID{}
	err = scope.Tx(ctx, func(tx *sqlx.Tx) error {
		if err := tx.GetContext(ctx, &harborConnID,
			`SELECT id FROM integration_connections
			  WHERE org_id = $1 AND provider = 'harbor_trivy' AND deleted_at IS NULL
			  ORDER BY created_at LIMIT 1`, org.ID); err != nil {
			return fmt.Errorf("no harbor_trivy connection in this organization (run `lotus seed-connections` first): %w", err)
		}
		rows, err := tx.QueryxContext(ctx,
			`SELECT c.slug, c.id FROM components c WHERE c.org_id = $1 AND c.archived_at IS NULL`, org.ID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var slug string
			var id uuid.UUID
			if err := rows.Scan(&slug, &id); err != nil {
				return err
			}
			componentBySlug[slug] = id
		}
		return rows.Err()
	})
	if err != nil {
		log.Fatalf("lotus seed-trivy: %v", err)
	}

	// Sort so that, per component, the image with the WORST counts is
	// written last. component_metrics_current keeps one headline per
	// (component, kind), so when a repo has a -dev and a -release image
	// the later write is what the dashboard shows — and the honest thing
	// for it to show is the worse of the two, not whichever happened to
	// come last in the file.
	snaps := export.Snapshots
	sort.SliceStable(snaps, func(i, j int) bool {
		// Same weighting the headline score uses, with the raw total as
		// the tie-break so two clean images still order by what they do
		// carry (mediums and lows) rather than by file position.
		wi := snaps[i].Critical*20 + snaps[i].High*5
		wj := snaps[j].Critical*20 + snaps[j].High*5
		if wi != wj {
			return wi < wj
		}
		return snaps[i].Total < snaps[j].Total
	})

	imported, skipped := 0, []string{}
	for _, s := range snaps {
		slug, ok := trivyImageToComponent[imageStem(s.Image)]
		componentID, found := componentBySlug[slug]
		if !ok || !found {
			skipped = append(skipped, s.Repo)
			continue
		}

		var bindingID uuid.UUID
		if err := scope.Tx(ctx, func(tx *sqlx.Tx) error {
			return tx.GetContext(ctx, &bindingID, `
				INSERT INTO metric_bindings (org_id, component_id, connection_id, kind, external_key, config)
				VALUES ($1, $2, $3, 'trivy', $4, '{}')
				ON CONFLICT (component_id, kind, external_key) WHERE deleted_at IS NULL
				DO UPDATE SET enabled = true
				RETURNING id`, org.ID, componentID, harborConnID, s.Repo)
		}); err != nil {
			log.Fatalf("lotus seed-trivy: binding %s: %v", s.Repo, err)
		}

		var tags []string
		for _, t := range strings.Split(s.Tags, ",") {
			if t = strings.TrimSpace(t); t != "" {
				tags = append(tags, t)
			}
		}
		pulls, artifacts := s.Pulls, s.ArtifactCount
		result, err := trivy.BuildImportedResult(trivy.ImportedSnapshot{
			Digest: s.Digest, Tags: tags, Severity: s.Severity,
			Counts: trivy.Counts{Total: s.Total, Fixable: s.Fixable, Critical: s.Critical,
				High: s.High, Medium: s.Medium, Low: s.Low, Unknown: s.Unknown},
			Pulls: &pulls, ArtifactCount: &artifacts,
			ImagePushedAt: s.ImagePushedAt, ScanFinishedAt: s.ScanFinishedAt,
		}, "critical")
		if err != nil {
			log.Fatalf("lotus seed-trivy: build %s: %v", s.Repo, err)
		}
		if err := trivy.Persist(ctx, scope, componentID, bindingID, result); err != nil {
			log.Fatalf("lotus seed-trivy: persist %s: %v", s.Repo, err)
		}
		if err := repo.InsertMetricFacts(ctx, scope, componentID, bindingID, "trivy", result); err != nil {
			log.Fatalf("lotus seed-trivy: facts %s: %v", s.Repo, err)
		}
		fmt.Printf("  %-46s -> %-28s crit=%d high=%d total=%d\n", s.Repo, slug, s.Critical, s.High, s.Total)
		imported++
	}

	fmt.Printf("\n%d image(s) imported.\n", imported)
	if len(skipped) > 0 {
		fmt.Printf("skipped %d (no component mapping): %s\n", len(skipped), strings.Join(skipped, ", "))
	}
}
