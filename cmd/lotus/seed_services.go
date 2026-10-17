package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"gopkg.in/yaml.v3"

	"github.com/uyab/lotus/internal/config"
	"github.com/uyab/lotus/internal/db"
	"github.com/uyab/lotus/internal/repo/global"
	"github.com/uyab/lotus/internal/tenant"
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
	// Sentry: false opts an AlurKerja service out of the shared Sentry
	// binding every other one gets — for a service that does not report
	// to Sentry yet, where the binding would only ever show no issues.
	Sentry *bool `yaml:"sentry"`
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

// runSeedServices imports the legacy services.yaml registry as projects,
// components and metric bindings.
//
// It began as a development seeder and was only ever run against a
// superuser connection, which is the one kind of connection Row-Level
// Security does not apply to. On a real deployment it could not have
// worked: the migrate role owns the tables but is not a superuser, FORCE
// ROW LEVEL SECURITY binds it, and with no app.org_id set every INSERT
// fails the tenant policy and every connection lookup comes back empty.
// It now runs as the application's own restricted role inside a
// tenant-scoped transaction, exactly as a request would — so it is safe to
// run on production, and a test can prove RLS is satisfied rather than
// bypassed.
//
// It creates no connections: inventing base URLs and credentials is not an
// importer's business, and a credential must go through the sealed
// connection flow. Bindings are created for whichever gitlab, sonarqube and
// sentry connections the organization already has, and skipped otherwise.
// Every write is idempotent, so running it again after adding a connection
// fills in exactly the bindings that were skipped.
func runSeedServices(cfg config.Config, args []string) {
	fs := flag.NewFlagSet("seed-services", flag.ExitOnError)
	path := fs.String("file", "", "path to the legacy services.yaml (required)")
	only := fs.String("only", "", "comma-separated products to import; default all")
	orgSlug := fs.String("org", "", "slug of the organization to import into; required outside LOTUS_DEV=1")
	if err := fs.Parse(args); err != nil {
		log.Fatal(err)
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

	// The application role, not the migrate role: this importer needs only
	// DML, and running under the same RLS the app runs under is the point.
	conn, err := db.OpenApp(cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("lotus seed-services: connect: %v", err)
	}
	defer conn.Close()
	ctx := context.Background()

	org, err := global.TheOnlyOrg(ctx, conn)
	if err != nil {
		log.Fatalf("lotus seed-services: %v (run `lotus init` first)", err)
	}
	// Outside dev, name the organization explicitly. It costs one flag and
	// means a command pasted into the wrong server's shell stops here
	// instead of writing into somebody else's instance.
	if !cfg.Dev && *orgSlug != org.Subdomain {
		log.Fatalf("lotus seed-services: this instance's organization is %q; pass --org=%s to confirm", org.Subdomain, org.Subdomain)
	}

	sum, err := importServices(ctx, conn, org.ID, reg, wanted)
	if err != nil {
		log.Fatalf("lotus seed-services: %v", err)
	}

	for _, p := range sum.Projects {
		fmt.Printf("%s (%d components)\n", displayName(p.Product), len(p.Components))
		for _, c := range p.Components {
			fmt.Printf("  %-34s %s\n", c.Name, c.Kind)
		}
	}
	fmt.Printf("\n%d projects, %d components, %d bindings created (%d already present).\n",
		len(sum.Projects), sum.Components, sum.BindingsCreated, sum.BindingsExisting)
	for _, kind := range []string{"gitlab", "sonarqube", "sentry"} {
		if !sum.Connected[kind] {
			fmt.Printf("note: no %s connection in this organization — those bindings were skipped. Add it in Settings → Connections and run this again.\n", kind)
		}
	}
	fmt.Println("note: services.yaml carries no Harbor/Trivy image data; a component with GitLab is scanned by repository instead, once the server adds its Trivy binding.")
}

type importedComponent struct{ Name, Kind string }

type importedProject struct {
	Product    string
	Components []importedComponent
}

type importSummary struct {
	Projects         []importedProject
	Components       int
	BindingsCreated  int
	BindingsExisting int
	Connected        map[string]bool
}

// importServices does the work in one tenant-scoped transaction: all of it
// lands, or none of it does.
func importServices(ctx context.Context, conn *sqlx.DB, orgID uuid.UUID, reg legacyRegistry, wanted map[string]bool) (importSummary, error) {
	sum := importSummary{Connected: map[string]bool{}}

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

	err := tenant.FromRun(conn, orgID).Tx(ctx, func(tx *sqlx.Tx) error {
		// Bind to whatever connection of each kind the organization already
		// has. Inside the scoped transaction, so RLS lets this see them.
		connIDs := map[string]uuid.UUID{}
		for _, provider := range []string{"gitlab", "sonarqube", "sentry"} {
			var id uuid.UUID
			err := tx.GetContext(ctx, &id,
				`SELECT id FROM integration_connections
				  WHERE org_id = $1 AND provider = $2 AND deleted_at IS NULL
				  ORDER BY created_at LIMIT 1`, orgID, provider)
			switch {
			case err == nil:
				connIDs[provider] = id
				sum.Connected[provider] = true
			case !errors.Is(err, sql.ErrNoRows):
				return fmt.Errorf("look up %s connection: %w", provider, err)
			}
		}

		for _, product := range products {
			var projectID uuid.UUID
			if err := tx.GetContext(ctx, &projectID, `
				INSERT INTO projects (org_id, slug, name) VALUES ($1, $2, $3)
				ON CONFLICT (org_id, slug) DO UPDATE SET name = EXCLUDED.name
				RETURNING id`, orgID, product, displayName(product)); err != nil {
				return fmt.Errorf("project %s: %w", product, err)
			}
			imported := importedProject{Product: product}

			for _, s := range grouped[product] {
				name := s.Label
				if name == "" {
					name = s.Name
				}
				// language is NOT NULL DEFAULT '', and an explicit NULL does
				// not fall back to the default — so a registry entry with no
				// language used to abort the whole import. Pass the empty
				// string the column already means "unknown" by.
				var componentID uuid.UUID
				if err := tx.GetContext(ctx, &componentID, `
					INSERT INTO components (org_id, project_id, slug, name, kind, language, legacy_name)
					VALUES ($1, $2, $3, $4, $5, $6, $7)
					ON CONFLICT (project_id, slug) DO UPDATE
					  SET name = EXCLUDED.name, kind = EXCLUDED.kind, language = EXCLUDED.language
					RETURNING id`,
					orgID, projectID, s.Name, name, s.Kind, s.Language, s.Name); err != nil {
					return fmt.Errorf("component %s: %w", s.Name, err)
				}
				sum.Components++
				imported.Components = append(imported.Components, importedComponent{Name: s.Name, Kind: s.Kind})

				// One binding per integration the registry carries a key
				// for. Sentry has no per-service field anywhere in the file,
				// so it uses the documented default: the shared project,
				// narrowed by a service_name tag equal to the service name.
				bindings := []struct{ kind, externalKey, cfg string }{
					{"gitlab", s.GitlabPat, `{}`},
					{"sonarqube", s.SonarKey, `{}`},
				}
				// Only AlurKerja's shared Sentry projects are known; no
				// entry sets sentry_project for the others, and inventing a
				// slug would only produce 404s that look like a broken
				// integration.
				if product == defaultProduct && (s.Sentry == nil || *s.Sentry) {
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
					res, err := tx.ExecContext(ctx, `
						INSERT INTO metric_bindings (org_id, component_id, connection_id, kind, external_key, config)
						VALUES ($1, $2, $3, $4, $5, $6::jsonb)
						ON CONFLICT DO NOTHING`,
						orgID, componentID, cid, b.kind, b.externalKey, b.cfg)
					if err != nil {
						return fmt.Errorf("binding %s/%s: %w", s.Name, b.kind, err)
					}
					if n, _ := res.RowsAffected(); n == 1 {
						sum.BindingsCreated++
					} else {
						sum.BindingsExisting++
					}
				}
			}
			sum.Projects = append(sum.Projects, imported)
		}
		return nil
	})
	return sum, err
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
