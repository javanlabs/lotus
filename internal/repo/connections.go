package repo

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/crypto"
	"github.com/uyab/lotus/internal/tenant"
)

// ErrNameTaken mirrors ErrSlugTaken for integration_connections' own
// unique key, (org_id, provider, name).
var ErrNameTaken = errors.New("connection name already taken for this provider in this organization")

// ErrSecretRequired is returned when an operation that must re-establish
// trust in a credential (task 2.2: changing base_url) is attempted without
// providing a new secret alongside it.
var ErrSecretRequired = errors.New("a new secret must be provided")

// Connection is deliberately missing any ciphertext, nonce, or plaintext
// secret field — task 2.2's "the type itself never carries the secret to
// the view layer" requirement. There is no field a handler could
// accidentally serialize even by mistake; DecryptConnectionSecret returns
// the plaintext separately, and only the test-connection code path ever
// calls it.
type Connection struct {
	ID                uuid.UUID       `db:"id"`
	OrgID             uuid.UUID       `db:"org_id"`
	Provider          string          `db:"provider"`
	Name              string          `db:"name"`
	BaseURL           string          `db:"base_url"`
	Config            json.RawMessage `db:"config"`
	SecretLast4       *string         `db:"secret_last4"`
	SecretFingerprint *string         `db:"secret_fingerprint"`
	SecretUpdatedAt   *time.Time      `db:"secret_updated_at"`
	Status            string          `db:"status"`
	LastVerifiedAt    *time.Time      `db:"last_verified_at"`
	LastError         *string         `db:"last_error"`
	DeletedAt         *time.Time      `db:"deleted_at"`
	CreatedAt         time.Time       `db:"created_at"`
}

const connectionColumns = `id, org_id, provider, name, base_url, config, secret_last4, secret_fingerprint,
	secret_updated_at, status, last_verified_at, last_error, deleted_at, created_at`

type NewConnection struct {
	Provider string
	Name     string
	BaseURL  string
	Config   json.RawMessage
	Secret   string
}

// CreateConnection seals Secret under dek (the caller's already-fetched,
// unwrapped org DEK — see internal/repo/global.GetOrCreateOrgDEK) with the
// org id as AAD, and stores only the ciphertext. dek is a parameter rather
// than something this function fetches itself because DEK storage lives on
// organizations, a control-plane table tenant.Scope has no path to — the
// caller (a handler holding both a Scope and the raw *sqlx.DB from Deps)
// is what bridges the two.
func CreateConnection(ctx context.Context, s tenant.Scope, dek []byte, in NewConnection) (*Connection, error) {
	ciphertext, nonce, err := crypto.Seal(dek, crypto.OrgAAD(s.OrgID()), []byte(in.Secret))
	if err != nil {
		return nil, err
	}
	fp := crypto.Fingerprint([]byte(in.Secret))
	last4 := last4(in.Secret)
	cfg := in.Config
	if len(cfg) == 0 {
		cfg = json.RawMessage(`{}`)
	}

	var out Connection
	err = s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.GetContext(ctx, &out, `
			INSERT INTO integration_connections
				(org_id, provider, name, base_url, config, secret_ciphertext, secret_nonce,
				 secret_fingerprint, secret_last4, secret_updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, now())
			RETURNING `+connectionColumns,
			s.OrgID(), in.Provider, in.Name, in.BaseURL, []byte(cfg), ciphertext, nonce, fp, last4)
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) {
			switch pgErr.Code {
			case "23505":
				return nil, ErrNameTaken
			case "23514":
				return nil, ErrInvalidInput
			}
		}
		return nil, err
	}
	return &out, nil
}

// ConnectionByID returns a live connection — deleted_at IS NULL. Callers
// that legitimately need a deleted row (none exist yet) would need a
// separate function, the same split every other soft-deletable table in
// this codebase uses.
func ConnectionByID(ctx context.Context, s tenant.Scope, id uuid.UUID) (*Connection, error) {
	var out Connection
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.GetContext(ctx, &out, `SELECT `+connectionColumns+` FROM integration_connections WHERE id = $1 AND deleted_at IS NULL`, id)
	})
	if err != nil {
		if isNoRows(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &out, nil
}

func ListConnections(ctx context.Context, s tenant.Scope) ([]Connection, error) {
	var out []Connection
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &out, `SELECT `+connectionColumns+` FROM integration_connections WHERE deleted_at IS NULL ORDER BY created_at`)
	})
	return out, err
}

// DeleteConnection soft-deletes a connection and every live binding on it,
// in one transaction — task 4.5's spec ("delete yang nampilin berapa
// binding bakal ikut mati") requires the count be shown before this is
// called (see CountActiveBindingsForConnection, bindings.go), and this
// function is what actually carries it out atomically: both the
// connection and its bindings disappear from every listing together, or
// neither does. Returns the number of bindings that were soft-deleted
// alongside it, for the caller's confirmation/audit message.
func DeleteConnection(ctx context.Context, s tenant.Scope, id uuid.UUID) (bindingsDeleted int, err error) {
	err = s.Tx(ctx, func(tx *sqlx.Tx) error {
		res, err := tx.ExecContext(ctx, `
			UPDATE integration_connections SET deleted_at = now() WHERE id = $1 AND deleted_at IS NULL`, id)
		if err != nil {
			return err
		}
		if err := checkRowsAffected(res); err != nil {
			return err
		}
		res, err = tx.ExecContext(ctx, `
			UPDATE metric_bindings SET enabled = false, deleted_at = now()
			WHERE connection_id = $1 AND deleted_at IS NULL`, id)
		if err != nil {
			return err
		}
		affected, err := res.RowsAffected()
		bindingsDeleted = int(affected)
		return err
	})
	if err != nil {
		if isNoRows(err) {
			return 0, ErrNotFound
		}
		return 0, err
	}
	return bindingsDeleted, nil
}

// UpdateConnectionMeta changes name/config only — never base_url or the
// secret. Renaming and reconfiguring a connection doesn't change what
// host it talks to or what credential it uses, so neither the SSRF guard
// nor an audit event applies here.
func UpdateConnectionMeta(ctx context.Context, s tenant.Scope, id uuid.UUID, name string, config json.RawMessage) (*Connection, error) {
	if len(config) == 0 {
		config = json.RawMessage(`{}`)
	}
	var out Connection
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.GetContext(ctx, &out, `
			UPDATE integration_connections SET name = $2, config = $3 WHERE id = $1
			RETURNING `+connectionColumns, id, name, []byte(config))
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return nil, ErrNameTaken
		}
		if isNoRows(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &out, nil
}

// UpdateConnectionSecret rotates the stored credential — the only way to
// change it; there is no "reveal current, edit, save" path (write-only).
func UpdateConnectionSecret(ctx context.Context, s tenant.Scope, dek []byte, id uuid.UUID, secret string) (*Connection, error) {
	if secret == "" {
		return nil, ErrSecretRequired
	}
	ciphertext, nonce, err := crypto.Seal(dek, crypto.OrgAAD(s.OrgID()), []byte(secret))
	if err != nil {
		return nil, err
	}
	fp := crypto.Fingerprint([]byte(secret))
	last4 := last4(secret)

	var out Connection
	err = s.Tx(ctx, func(tx *sqlx.Tx) error {
		if err := tx.GetContext(ctx, &out, `
			UPDATE integration_connections
			SET secret_ciphertext = $2, secret_nonce = $3, secret_fingerprint = $4,
			    secret_last4 = $5, secret_updated_at = now(),
			    status = 'pending', last_verified_at = NULL, last_error = NULL
			WHERE id = $1
			RETURNING `+connectionColumns, id, ciphertext, nonce, fp, last4); err != nil {
			return err
		}
		return unfreezeBindings(ctx, tx, id)
	})
	if err != nil {
		if isNoRows(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &out, nil
}

// UpdateConnectionBaseURLWithSecret changes base_url and rotates the
// secret in the same statement — task 2.2's "perubahan base_url ...
// ngewajibin credential dimasukin ulang", enforced structurally by this
// being the *only* function that can change base_url at all (there is no
// UpdateConnectionBaseURL that skips the secret). The audit_events row the
// spec also requires is written by the caller in the same handler
// transaction-adjacent step (see internal/web/connections.go) — audit_events
// is a plain tenant-scoped table with no special coupling to this repo
// function, so no reason to entangle them here.
func UpdateConnectionBaseURLWithSecret(ctx context.Context, s tenant.Scope, dek []byte, id uuid.UUID, baseURL, secret string) (*Connection, error) {
	if secret == "" {
		return nil, ErrSecretRequired
	}
	ciphertext, nonce, err := crypto.Seal(dek, crypto.OrgAAD(s.OrgID()), []byte(secret))
	if err != nil {
		return nil, err
	}
	fp := crypto.Fingerprint([]byte(secret))
	last4 := last4(secret)

	var out Connection
	err = s.Tx(ctx, func(tx *sqlx.Tx) error {
		if err := tx.GetContext(ctx, &out, `
			UPDATE integration_connections
			SET base_url = $2, secret_ciphertext = $3, secret_nonce = $4, secret_fingerprint = $5,
			    secret_last4 = $6, secret_updated_at = now(),
			    status = 'pending', last_verified_at = NULL, last_error = NULL
			WHERE id = $1
			RETURNING `+connectionColumns, id, baseURL, ciphertext, nonce, fp, last4); err != nil {
			return err
		}
		return unfreezeBindings(ctx, tx, id)
	})
	if err != nil {
		if isNoRows(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &out, nil
}

// DecryptConnectionSecret returns the plaintext credential. The only
// legitimate caller is the test-connection code path (task 2.2) and,
// later, Fase 3's collectors — never a handler that puts the result
// anywhere in an HTTP response.
func DecryptConnectionSecret(ctx context.Context, s tenant.Scope, dek []byte, id uuid.UUID) (string, error) {
	var row struct {
		Ciphertext []byte `db:"secret_ciphertext"`
		Nonce      []byte `db:"secret_nonce"`
	}
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.GetContext(ctx, &row, `
			SELECT secret_ciphertext, secret_nonce FROM integration_connections WHERE id = $1`, id)
	})
	if err != nil {
		if isNoRows(err) {
			return "", ErrNotFound
		}
		return "", err
	}
	plaintext, err := crypto.Open(dek, crypto.OrgAAD(s.OrgID()), row.Ciphertext, row.Nonce)
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}

// SetConnectionTestResult records a test-connection outcome (task 2.2:
// "Simpan hasilnya ke status + last_verified_at + last_error"). status
// must be one of the CHECK-constrained values: "ok", "auth_failed", or
// "error". Anything other than "ok" freezes every binding on this
// connection — task 2.3's binding scheduler is expected to skip bindings
// whose connection isn't 'ok', not something this function does itself.
func SetConnectionTestResult(ctx context.Context, s tenant.Scope, id uuid.UUID, status, errMsg string) error {
	ok := status == "ok"
	var lastErr *string
	if !ok {
		lastErr = &errMsg
	}
	return s.Tx(ctx, func(tx *sqlx.Tx) error {
		res, err := tx.ExecContext(ctx, `
			UPDATE integration_connections
			SET status = $2, last_verified_at = CASE WHEN $3 THEN now() ELSE last_verified_at END, last_error = $4
			WHERE id = $1`, id, status, ok, lastErr)
		if err != nil {
			return err
		}
		return checkRowsAffected(res)
	})
}

func last4(secret string) *string {
	if secret == "" {
		return nil
	}
	if len(secret) <= 4 {
		return &secret
	}
	v := secret[len(secret)-4:]
	return &v
}
