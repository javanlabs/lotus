package global

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

// OrgSettings returns organizations.settings — a generic jsonb bag (task
// 0.2) for org-level policy knobs that don't belong on any one binding.
// Control-plane, unscoped, same as everything else in this package.
func OrgSettings(ctx context.Context, db *sqlx.DB, orgID uuid.UUID) (json.RawMessage, error) {
	var raw []byte
	if err := db.GetContext(ctx, &raw, `SELECT settings FROM organizations WHERE id = $1`, orgID); err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return json.RawMessage(`{}`), nil
	}
	return raw, nil
}

// UpdateOrgSettingsKey merges {key: value} into organizations.settings —
// task 3.4's trivy_fail_on lives here, set via a future admin settings
// page (task 4.5, not yet built); this is the write path it'll call.
func UpdateOrgSettingsKey(ctx context.Context, db *sqlx.DB, orgID uuid.UUID, key string, value any) error {
	valueJSON, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `
		UPDATE organizations SET settings = jsonb_set(settings, $2::text[], $3::jsonb, true) WHERE id = $1`,
		orgID, "{"+key+"}", string(valueJSON))
	return err
}
