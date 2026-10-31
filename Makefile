TAILWIND = bin/tailwindcss

.PHONY: css watch-css dev dev-prod db-pull-prod run-once build build-linux run migrate-up migrate-down db-up db-down test e2e e2e-db lint

css: $(TAILWIND)
	$(TAILWIND) -i web/static/css/app.css -o web/static/css/app.built.css --minify

# Local Postgres for dev & test. Skips docker entirely when a server is
# already reachable at the dev DSN, so this also works on machines without
# docker installed. lotus and lotus_test are both created if missing.
db-up:
	@if pg_isready -h localhost -p 5418 -U postgres >/dev/null 2>&1; then \
		: ; \
	elif command -v docker >/dev/null 2>&1; then \
		docker compose -f deploy/docker-compose.dev.yml up -d; \
		until docker exec lotus-pg pg_isready -U postgres >/dev/null 2>&1; do sleep 1; done; \
	else \
		echo "postgres unreachable at localhost:5418 and docker is not installed" >&2; \
		exit 1; \
	fi
	@for db in lotus lotus_test; do \
	  psql "postgres://postgres@localhost:5418/postgres?sslmode=disable" -tc \
	    "SELECT 1 FROM pg_database WHERE datname='$$db'" | grep -q 1 || \
	    psql "postgres://postgres@localhost:5418/postgres?sslmode=disable" -c "CREATE DATABASE $$db"; \
	done
	@echo "postgres ready on :5418 (lotus, lotus_test)"

db-down:
	docker compose -f deploy/docker-compose.dev.yml down

migrate-up: db-up
	MIGRATE_DATABASE_URL=postgres://postgres@localhost:5418/lotus?sslmode=disable go run ./cmd/lotus migrate up

migrate-down: db-up
	MIGRATE_DATABASE_URL=postgres://postgres@localhost:5418/lotus?sslmode=disable go run ./cmd/lotus migrate down

test: db-up
	TEST_DATABASE_URL=postgres://postgres@localhost:5418/lotus_test?sslmode=disable go test ./...

# The browser suite gets its OWN migrated database, never the dev one.
# Two reasons, both learned the hard way:
#   - it seeds an organization per test, and more than one live org makes
#     LOTUS_MODE=single refuse to boot (global.TheOnlyOrg);
#   - lotus_test is no good either — the Go tests build a schema per test
#     there, so its public schema has no tables for a server to serve.
# It also needs saas mode, because the specs address tenants as
# <subdomain>.lotus.localhost. Both settings are passed in the environment,
# which takes precedence over .env.
E2E_DSN = postgres://postgres@localhost:5418/lotus_e2e?sslmode=disable
PG_ADMIN_DSN = postgres://postgres@localhost:5418/postgres?sslmode=disable

e2e-db: db-up
	@psql "$(PG_ADMIN_DSN)" -tc "SELECT 1 FROM pg_database WHERE datname='lotus_e2e'" \
	  | grep -q 1 || psql "$(PG_ADMIN_DSN)" -c "CREATE DATABASE lotus_e2e"
	@MIGRATE_DATABASE_URL=$(E2E_DSN) go run ./cmd/lotus migrate up

e2e: e2e-db
	cd e2e && DATABASE_URL=$(E2E_DSN) LOTUS_MODE=saas npx playwright test

lint:
	go vet ./...
	./scripts/check-repo-scope.sh

watch-css: $(TAILWIND)
	$(TAILWIND) -i web/static/css/app.css -o web/static/css/app.built.css --watch

# Picks the right standalone Tailwind CLI binary for the host running make
# (macOS arm64 for local dev, linux-x64 in CI) instead of hardcoding one.
$(TAILWIND):
	mkdir -p bin
	@os=$$(uname -s); arch=$$(uname -m); \
	case "$$os-$$arch" in \
		Darwin-arm64)  asset=tailwindcss-macos-arm64 ;; \
		Darwin-x86_64) asset=tailwindcss-macos-x64 ;; \
		Linux-x86_64)  asset=tailwindcss-linux-x64 ;; \
		Linux-aarch64) asset=tailwindcss-linux-arm64 ;; \
		*) echo "unsupported platform $$os-$$arch for standalone tailwindcss" >&2; exit 1 ;; \
	esac; \
	curl -sL -o $(TAILWIND) "https://github.com/tailwindlabs/tailwindcss/releases/latest/download/$$asset"
	chmod +x $(TAILWIND)

# Local dev with hot-reload: air rebuilds+restarts the server on Go changes,
# tailwind --watch regenerates CSS, and LOTUS_DEV=1 serves templates/static
# from disk so they hot-reload without a rebuild. Ctrl-C stops both.
dev: $(TAILWIND) db-up
	@LOTUS_DEV=1 bash -c 'trap "kill 0" EXIT; \
	  $(TAILWIND) -i web/static/css/app.css -o web/static/css/app.built.css --watch & \
	  go run github.com/air-verse/air@latest'

# A local copy of production for testing against real data; see the script.
# Production is only read. Re-run to refresh, which replaces the copy.
db-pull-prod: db-up
	./scripts/db-pull-prod.sh

# `make dev` on that copy. Sign in at /login with your own address: under
# LOTUS_DEV the magic link is printed on the page. Set in the environment,
# which takes precedence over .env.
PROD_COPY_DSN = postgres://postgres@localhost:5418/lotus_prod_copy?sslmode=disable
dev-prod:
	@DATABASE_URL="$(PROD_COPY_DSN)" MIGRATE_DATABASE_URL="$(PROD_COPY_DSN)" \
	  LOTUS_MODE=single LOTUS_URL=http://localhost:8008 ADDR=:8008 $(MAKE) dev

# One-shot run without hot-reload.
run-once: css db-up
	LOTUS_DEV=1 go run ./cmd/lotus serve

build: css
	go build -o bin/lotus ./cmd/lotus

# Production Linux binary (self-contained: embeds templates+static, pure-Go
# pgx driver for Postgres, static link). Same build the CI pipeline runs.
build-linux: css
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o bin/lotus-linux ./cmd/lotus

run: build
	./bin/lotus serve
