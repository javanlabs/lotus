package global

import (
	"context"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/crypto"
)

// GetOrCreateOrgDEK returns orgID's unwrapped Data Encryption Key,
// generating and wrapping a fresh one under kek on first use. organizations
// is control-plane (no RLS), and DEK creation is lazy rather than done at
// signup because most organizations won't add a credential on day one —
// this keeps org creation itself free of any crypto dependency.
//
// Race-safety: two concurrent first-credential-saves for the same org must
// not each generate and wrap a different DEK (the loser's credential would
// then be undecryptable under the DEK that won). SELECT ... FOR UPDATE
// holds a row lock for the duration of the transaction, so the second
// caller blocks until the first commits and then sees the DEK it just
// wrote, instead of racing it.
func GetOrCreateOrgDEK(ctx context.Context, db *sqlx.DB, kek *crypto.KEK, orgID uuid.UUID) ([]byte, error) {
	tx, err := db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck

	var row struct {
		Ciphertext []byte `db:"dek_ciphertext"`
		Nonce      []byte `db:"dek_nonce"`
	}
	if err := tx.GetContext(ctx, &row, `
		SELECT dek_ciphertext, dek_nonce FROM organizations WHERE id = $1 FOR UPDATE`, orgID); err != nil {
		return nil, err
	}

	if row.Ciphertext == nil {
		dek, err := crypto.NewDEK()
		if err != nil {
			return nil, err
		}
		ciphertext, nonce, err := kek.WrapDEK(dek)
		if err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE organizations SET dek_ciphertext = $2, dek_nonce = $3, dek_kek_version = $4 WHERE id = $1`,
			orgID, ciphertext, nonce, kek.Version); err != nil {
			return nil, err
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return dek, nil
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return kek.UnwrapDEK(row.Ciphertext, row.Nonce)
}

// RewrapOrgDEKs re-wraps every organization's DEK from oldKEK to newKEK —
// task 2.2's "perintah re-wrap jalan tanpa downtime baca": no credential
// row is touched, only organizations.dek_ciphertext/dek_nonce/dek_kek_version,
// so every already-open read transaction keeps working against unchanged
// credential ciphertexts throughout.
func RewrapOrgDEKs(ctx context.Context, db *sqlx.DB, oldKEK, newKEK *crypto.KEK) (int, error) {
	type orgRow struct {
		ID         uuid.UUID `db:"id"`
		Ciphertext []byte    `db:"dek_ciphertext"`
		Nonce      []byte    `db:"dek_nonce"`
	}
	var orgs []orgRow
	if err := db.SelectContext(ctx, &orgs, `
		SELECT id, dek_ciphertext, dek_nonce FROM organizations WHERE dek_ciphertext IS NOT NULL`); err != nil {
		return 0, err
	}

	n := 0
	for _, o := range orgs {
		dek, err := oldKEK.UnwrapDEK(o.Ciphertext, o.Nonce)
		if err != nil {
			return n, err
		}
		ciphertext, nonce, err := newKEK.WrapDEK(dek)
		if err != nil {
			return n, err
		}
		if _, err := db.ExecContext(ctx, `
			UPDATE organizations SET dek_ciphertext = $2, dek_nonce = $3, dek_kek_version = $4 WHERE id = $1`,
			o.ID, ciphertext, nonce, newKEK.Version); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}
