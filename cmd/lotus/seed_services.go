package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"

	"github.com/google/uuid"
	"gopkg.in/yaml.v3"

	"github.com/uyab/lotus/internal/config"
	"github.com/uyab/lotus/internal/db"
	"github.com/uyab/lotus/internal/repo/global"
)

// legacyService is the subset of the old services.yaml registry that maps
// onto the current model. The file documents more fields than it actually
// sets — sentry_service, sentry_project, fs_scan and others appear only in
// its header comment, never in a single entry — so anything not read here
// genuinely has no data behind it.
type legacyService struct {
	Name      string `yaml:"name"`
	Label     string `yaml:"label"`
	Product   string `yaml:"product"`
	Kind      string `yaml:"kind"`
	Language  string `yaml:"language"`
	GitlabPat string `yaml:"gitlab_path"`
	SonarKey  string `yaml:"sonar_key"`
}

type legacyRegistry struct {
	Services []legacyService `yaml:"services"`
}

// AlurKerja ships every backend into one shared Sentry project and every
// frontend into another, told apart by a service_name tag rather than by
// project — the registry's own header says so, and the legacy deployment
// carried these as SENTRY_PROJECT_BACKEND / _FRONTEND / _SERVICE_TAG.
//
// This is why the Sentry binding's external_key is a SHARED project slug
// and not the service name: the collector treats external_key as a Sentry
// project slug, so naming the service there asks for a project that does
// not exist (it 404s), and the tag is what narrows it down.
const (
	sentryProjectBackend  = "alurkerja-javan-be"
	sentryProjectFrontend = "alurkerja-javan-fe"
	sentryServiceTagKey   = "service_name"
)

// defaultProduct is what an entry with no `product:` belongs to — the
// registry's own header states this default, and 17 of 22 entries rely on
// it.
const defaultProduct = "alurkerja"

// runSeedServices turns the legacy services.yaml registry into projects,
// components, and metric bindings. This is a development seeder, not the
// real task 5.1 importer: it creates no connections of its own (it binds
// to whatever gitlab/sonarqube/sentry connection the org already has) and
// imports no historical metrics.
func runSeedServices(cfg config.Config, args []string) {
	fs := flag.NewFlagSet("seed-services", flag.ExitOnError)
	path := fs.String("file", "", "path to the legacy services.yaml (required)")
	only := fs.String("only", "", "comma-separated products to import; default all")
	if err := fs.Parse(args); err != nil {
		log.Fatal(err)
	}
	if !cfg.Dev {
		log.Fatal("lotus seed-services: refusing to run without LOTUS_DEV=1 — this writes fixture data")
	}
	if *path == "" {
		fs.Usage()
		log.Fatal("lotus seed-services: --file is required")
	}

	raw, err := os.ReadFile(*path)
	if err != nil {
		log.Fatalf("lotus seed-services: read %s: %v", *path, err)
	}
	var reg legacyRegistry
	if err := yaml.Unmarshal(raw, &reg); err != nil {
		log.Fatalf("lotus seed-services: parse %s: %v", *path, err)
	}
	if len(reg.Services) == 0 {
		log.Fatalf("lotus seed-services: %s has no services", *path)
	}

	wanted := map[string]bool{}
	for _, p := range strings.Split(*only, ",") {
		if p = strings.TrimSpace(p); p != "" {
			wanted[p] = true
		}
	}

	conn, err := db.Open(cfg.MigrateDatabaseURL)
	if err != nil {
		log.Fatalf("lotus seed-services: connect: %v", err)
	}
	defer conn.Close()
	ctx := context.Background()

	org, err := global.TheOnlyOrg(ctx, conn)
	if err != nil {
		log.Fatalf("lotus seed-services: %v (run `lotus init` first)", err)
	}

	// Bind to whatever connection of each kind the organization already
	// has. Creating connections here would mean inventing base URLs and
	// credentials, which a seeder has no business doing.
	connIDs := map[string]uuid.UUID{}
	for _, pv := range []struct{ provider, kind string }{
		{"gitlab", "gitlab"}, {"sonarqube", "sonarqube"}, {"sentry", "sentry"},
	} {
		var id uuid.UUID
		err := conn.GetContext(ctx, &id,
			`SELECT id FROM integration_connections
			  WHERE org_id = $1 AND provider = $2 AND deleted_at IS NULL
			  ORDER BY created_at LIMIT 1`, org.ID, pv.provider)
		if err == nil {
			connIDs[pv.kind] = id
		}
	}

	grouped := map[string][]legacyService{}
	for _, s := range reg.Services {
		p := s.Product
		if p == "" {
			p = defaultProduct
		}
		if len(wanted) > 0 && !wanted[p] {
			continue
		}
		grouped[p] = append(grouped[p], s)
	}
	products := make([]string, 0, len(grouped))
	for p := range grouped {
		products = append(products, p)
	}
	sort.Strings(products)

	totalComponents, totalBindings := 0, 0
	for _, product := range products {
		var projectID uuid.UUID
		if err := conn.GetContext(ctx, &projectID, `
			INSERT INTO projects (org_id, slug, name) VALUES ($1, $2, $3)
			ON CONFLICT (org_id, slug) DO UPDATE SET name = EXCLUDED.name
			RETURNING id`, org.ID, product, displayName(product)); err != nil {
			log.Fatalf("lotus seed-services: project %s: %v", product, err)
		}
		fmt.Printf("%s (%d components)\n", displayName(product), len(grouped[product]))

		for _, s := range grouped[product] {
			name := s.Label
			if name == "" {
				name = s.Name
			}
			var componentID uuid.UUID
			if err := conn.GetContext(ctx, &componentID, `
				INSERT INTO components (org_id, project_id, slug, name, kind, language, legacy_name)
				VALUES ($1, $2, $3, $4, $5, $6, $7)
				ON CONFLICT (project_id, slug) DO UPDATE
				  SET name = EXCLUDED.name, kind = EXCLUDED.kind, language = EXCLUDED.language
				RETURNING id`,
				org.ID, projectID, s.Name, name, s.Kind, nullIfEmpty(s.Language), s.Name); err != nil {
				log.Fatalf("lotus seed-services: component %s: %v", s.Name, err)
			}
			totalComponents++

			// One binding per integration the registry actually carries a
			// key for. Sentry has no per-service field set anywhere in the
			// file, so it falls back to the documented default: the shared
			// project, separated by a service_name tag equal to the
			// service's own name.
			bindings := []struct{ kind, externalKey, cfg string }{
				{"gitlab", s.GitlabPat, `{}`},
				{"sonarqube", s.SonarKey, `{}`},
			}
			// Only AlurKerja's shared Sentry projects are known. The header
			// claims SyarQ and AOS Online each have their own via
			// `sentry_project`, but no entry in the file actually sets it,
			// so there is nothing to bind them to — inventing a slug would
			// just produce 404s that look like a broken integration.
			if product == defaultProduct {
				project := sentryProjectBackend
				if s.Kind == "frontend" {
					project = sentryProjectFrontend
				}
				bindings = append(bindings, struct{ kind, externalKey, cfg string }{
					"sentry", project,
					fmt.Sprintf(`{"mode":"shared","service_tag_key":%q,"service_tag_value":%q}`,
						sentryServiceTagKey, s.Name),
				})
			}
			for _, b := range bindings {
				cid, ok := connIDs[b.kind]
				if !ok || b.externalKey == "" {
					continue
				}
				if _, err := conn.ExecContext(ctx, `
					INSERT INTO metric_bindings (org_id, component_id, connection_id, kind, external_key, config)
					VALUES ($1, $2, $3, $4, $5, $6::jsonb)
					ON CONFLICT DO NOTHING`,
					org.ID, componentID, cid, b.kind, b.externalKey, b.cfg); err != nil {
					log.Fatalf("lotus seed-services: binding %s/%s: %v", s.Name, b.kind, err)
				}
				totalBindings++
			}
			fmt.Printf("  %-34s %s\n", s.Name, s.Kind)
		}
	}

	fmt.Printf("\n%d projects, %d components, %d bindings.\n", len(products), totalComponents, totalBindings)
	for _, kind := range []string{"gitlab", "sonarqube", "sentry"} {
		if _, ok := connIDs[kind]; !ok {
			fmt.Printf("note: no %s connection in this organization — those bindings were skipped.\n", kind)
		}
	}
	fmt.Println("note: services.yaml carries no Harbor/Trivy image data, so no trivy bindings were created.")
}

func nullIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// displayName turns a registry product key into something presentable.
// Only the handful of known products get a curated label; anything else
// falls back to the key itself rather than a mangled guess.
func displayName(product string) string {
	switch product {
	case "alurkerja":
		return "AlurKerja"
	case "syarq":
		return "SyarQ"
	case "aosonline":
		return "AOS Online"
	default:
		return product
	}
}
