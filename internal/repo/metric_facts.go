package repo

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/collector"
	"github.com/uyab/lotus/internal/tenant"
)

// InsertMetricFacts is task 3.7's generic write path — called once per
// successful Collect, for every kind, from the one place that already
// orchestrates persistence (internal/scheduler's worker). It never
// switches on kind: every Fact task 3.1 already requires a collector to
// emit (r.Facts, plus every r.Children[i].Facts recursively — Sentry's
// own per-environment fan-out puts its Facts on Children, not the top
// level) is flattened and written the same way, and the catalog
// (metric_keys) is seeded/updated from the exact same flattened list —
// "generic code only reads Headline" (task 3.6) has a sibling rule here:
// generic code only reads Facts, never Detail.
func InsertMetricFacts(ctx context.Context, s tenant.Scope, componentID, bindingID uuid.UUID, kind string, r collector.Result) error {
	facts := flattenFacts(r)
	now := time.Now()
	if at, ok := observedAt(ctx); ok {
		now = at
	}

	return s.Tx(ctx, func(tx *sqlx.Tx) error {
		if err := seedDeclaredMetricKeys(ctx, tx, s.OrgID(), kind); err != nil {
			return err
		}
		seen := make(map[string]bool, len(facts))
		for _, f := range facts {
			if !seen[f.Key] {
				seen[f.Key] = true
				if err := upsertObservedMetricKey(ctx, tx, s.OrgID(), kind, f.Key, f.Unit, now); err != nil {
					return err
				}
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO metric_facts (org_id, binding_id, component_id, key, value_num, value_text, unit, collected_at)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
				s.OrgID(), bindingID, componentID, f.Key, f.ValueNum, f.ValueText, f.Unit, now); err != nil {
				return err
			}
		}
		return nil
	})
}

// flattenFacts walks r and every descendant Child, collecting every
// Fact — mirrors collector.ValidateResult's own recursive walk, for the
// same reason: a Result with Children carries its Facts on the leaves,
// not the root.
func flattenFacts(r collector.Result) []collector.Fact {
	if len(r.Children) == 0 {
		return r.Facts
	}
	var out []collector.Fact
	for _, c := range r.Children {
		out = append(out, flattenFacts(c)...)
	}
	return out
}

// seedDeclaredMetricKeys upserts kind's own Descriptor.MetricKeys() —
// the catalog's declared=true rows — so a metric appears with its
// friendly label/unit/direction from the very first successful collect
// of that kind for this org, not just once a fact for it happens to
// arrive. Cheap and idempotent (ON CONFLICT DO UPDATE), so running it on
// every collect rather than once at some setup step is simplest: there
// is no other DB-backed hook in a collector's lifecycle to seed from
// instead (registration, task 3.1's own init(), has no DB access).
func seedDeclaredMetricKeys(ctx context.Context, tx *sqlx.Tx, orgID uuid.UUID, kind string) error {
	c, ok := collector.Get(collector.Kind(kind))
	if !ok {
		return nil
	}
	for _, decl := range c.Descriptor().MetricKeys {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO metric_keys (org_id, kind, key, label, unit, value_type, direction, declared, first_seen_at, last_seen_at, active)
			VALUES ($1, $2, $3, $4, $5, $6, $7, true, now(), now(), true)
			ON CONFLICT (org_id, kind, key) DO UPDATE SET
				label = EXCLUDED.label, unit = EXCLUDED.unit, value_type = EXCLUDED.value_type, direction = EXCLUDED.direction,
				declared = true`,
			orgID, kind, decl.Key, decl.Label, decl.Unit, valueTypeOrDefault(decl.ValueType), directionOrDefault(decl.Direction)); err != nil {
			return err
		}
	}
	return nil
}

// upsertObservedMetricKey records that key was actually emitted just
// now — the catalog's own "can grow" half. A key outside the kind's
// declared set still lands here (declared defaults to false on first
// insert and is left alone on conflict, so seedDeclaredMetricKeys — run
// first, in the same transaction — always wins if the key is in fact
// declared).
func upsertObservedMetricKey(ctx context.Context, tx *sqlx.Tx, orgID uuid.UUID, kind, key, unit string, now time.Time) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO metric_keys (org_id, kind, key, unit, declared, first_seen_at, last_seen_at, active)
		VALUES ($1, $2, $3, $4, false, $5, $5, true)
		ON CONFLICT (org_id, kind, key) DO UPDATE SET
			first_seen_at = LEAST(metric_keys.first_seen_at, $5),
			last_seen_at = GREATEST(metric_keys.last_seen_at, $5), active = true,
			unit = CASE WHEN metric_keys.unit = '' THEN EXCLUDED.unit ELSE metric_keys.unit END`,
		orgID, kind, key, unit, now)
	return err
}

func valueTypeOrDefault(v string) string {
	if v == "" {
		return "number"
	}
	return v
}

func directionOrDefault(v string) string {
	if v == "" {
		return "neutral"
	}
	return v
}

// MetricKey is one catalog row.
type MetricKey struct {
	OrgID       uuid.UUID `db:"org_id"`
	Kind        string    `db:"kind"`
	Key         string    `db:"key"`
	Label       string    `db:"label"`
	Unit        string    `db:"unit"`
	ValueType   string    `db:"value_type"`
	Direction   string    `db:"direction"`
	Declared    bool      `db:"declared"`
	FirstSeenAt time.Time `db:"first_seen_at"`
	LastSeenAt  time.Time `db:"last_seen_at"`
	Active      bool      `db:"active"`
}

// ListMetricKeys returns every catalog entry for kind in this org
// (active and inactive) — the UI's own "pick a metric" source, and how
// it tells "discontinued" apart from "never collected".
func ListMetricKeys(ctx context.Context, s tenant.Scope, kind string) ([]MetricKey, error) {
	var out []MetricKey
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &out, `
			SELECT org_id, kind, key, label, unit, value_type, direction, declared, first_seen_at, last_seen_at, active
			FROM metric_keys WHERE org_id = $1 AND kind = $2 ORDER BY key`, s.OrgID(), kind)
	})
	return out, err
}
