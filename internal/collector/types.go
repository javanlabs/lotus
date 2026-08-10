// Package collector defines task 3.1's single shape every integration
// implements: Collector/Session for the pull direction (GitLab, SonarQube,
// Harbor), Ingestor for the push direction (Trivy CI ingest, task 3.4).
// Both directions produce the same Result, so storage, headline
// extraction, and the dashboard (task 3.6) sit one layer below both and
// never know which direction the data came from.
//
// Adding a fifth integration means writing a Collector (or Ingestor) and
// registering it — nothing else. Checklist:
//  1. Implement Collector (Descriptor + Open) and Session in a new package.
//  2. Register it in that package's init().
//  3. Add the kind to the metric_bindings.kind / connections.provider CHECK
//     constraints — the one migration.
//  4. Import the new package from cmd/lotus's main so its init() runs.
//  5. Nothing else — the scheduler (3.2), credential store (2.2), RLS
//     (0.3), discovery (2.4), and onboarding UI (4.3) all already work
//     against the Collector interface, not against any specific kind.
package collector

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/google/uuid"
)

// Kind identifies which integration a Collector/Ingestor implements.
// Deliberately the same string space as metric_bindings.kind (task 2.3) —
// "trivy", not connections.provider's "harbor_trivy"; see
// internal/integrations.BindingKindForProvider for the one place that
// distinction is bridged.
type Kind string

// These four are the fixed kind space (task 2.3's own metric_bindings.kind
// CHECK constraint) — "trivy", not connections.provider's "harbor_trivy";
// see internal/integrations.BindingKindForProvider for where that
// distinction is bridged.
const (
	KindGitLab    Kind = "gitlab"
	KindSonarQube Kind = "sonarqube"
	KindTrivy     Kind = "trivy"
	KindSentry    Kind = "sentry"
)

// ConnConfig is what Open needs to establish a Session: the connection's
// base_url and already-decrypted secret (the caller — the scheduler or a
// synchronous handler — is responsible for decryption; a Collector never
// touches ciphertext or the DEK), plus an HTTP client that already carries
// task 2.2's SSRF guard and, once wired in task 3.2, a per-connection rate
// limiter on its Transport.
type ConnConfig struct {
	BaseURL string
	Secret  string
	Client  *http.Client
}

type DiscoverQuery struct {
	Search string
}

// Candidate mirrors internal/integrations.DiscoveredItem's shape — kept as
// a separate type rather than a shared one so Session's contract doesn't
// depend on internal/integrations, which task 3.1's own goal (a fifth
// integration touches nothing but its own package) would otherwise
// contradict.
type Candidate struct {
	ExternalKey string
	Label       string
	Metadata    map[string]any
}

type BindingConfig struct {
	ExternalKey string
	Config      json.RawMessage
}

type Resolved struct {
	OK      bool
	Summary string
}

// CollectRequest is one binding's worth of work. State is the previous
// Collect's own State, round-tripped — a collector's watermark (e.g. "last
// commit SHA already counted"), opaque to everything except that
// collector.
type CollectRequest struct {
	OrgID       uuid.UUID
	ComponentID uuid.UUID
	BindingID   uuid.UUID
	ExternalKey string
	Config      json.RawMessage
	State       json.RawMessage
}

type Status string

const (
	StatusPass    Status = "pass"
	StatusWarn    Status = "warn"
	StatusFail    Status = "fail"
	StatusUnknown Status = "unknown"
)

// Headline is task 3.6's Layer 1: normalized, mandatory, the only thing
// generic code (dashboard, rollups, org score) is allowed to read.
type Headline struct {
	Status     Status
	Score      *int
	Grade      string
	CountValue *int64
	CountLabel string
	Summary    string
	DeepLink   string
}

// Fact is one measured metric — task 3.6's Layer 2, task 3.7's storage
// unit. Key is namespaced ("sonar.coverage", "trivy.critical"). Exactly
// one of ValueNum/ValueText should be set depending on the metric's
// nature; Unit is free text ("%", "count", "").
type Fact struct {
	Key       string
	ValueNum  *float64
	ValueText *string
	Unit      string
}

// Warning is a non-fatal sub-call failure — task 3.1's core rule made a
// type: partial failure is a first-class result, not an error. A Result
// with Warnings still gets a status of "partial", not "failed", and
// whatever it did collect is still stored — see the Fact-omission rule on
// Result itself.
type Warning struct {
	Step string
	Err  string
}

// Result is what one Collect (or Ingestor.Parse) call returns.
//
// Facts must never include a zero-value placeholder for a metric a
// sub-call failed to fetch — omit the Fact entirely and add a Warning
// instead. Writing zero makes a single flaky API call look like an
// incident on every graph built from Facts; this is the single
// most-violated rule in the interface, called out explicitly rather than
// left to convention.
type Result struct {
	Headline   Headline
	Facts      []Fact
	Detail     json.RawMessage
	DetailKind string
	Children   []Result
	Warnings   []Warning
	State      json.RawMessage
}

// ValidateResult enforces the one structural rule Collect's return value
// must satisfy: every leaf result (no Children) needs at least one Fact.
// A Result with Children needn't carry Facts itself — Sentry's per-
// environment fan-out (task 3.5) puts every Fact on a Child — but each
// Child is checked the same way, recursively. The scheduler (task 3.2)
// calls this on every successful Collect and treats a violation as a
// collector bug, not a run failure to retry.
func ValidateResult(r Result) error {
	if len(r.Children) == 0 {
		if len(r.Facts) == 0 {
			return errEmptyFacts
		}
		return nil
	}
	for _, child := range r.Children {
		if err := ValidateResult(child); err != nil {
			return err
		}
	}
	return nil
}

var errEmptyFacts = &collectorBugError{"collector: a leaf Result must include at least one Fact"}

type collectorBugError struct{ msg string }

func (e *collectorBugError) Error() string { return e.msg }

// Field declares one input a connection or binding form needs — task
// 3.1's mechanism for rendering every integration's settings form from
// data, the same pattern internal/integrations.FormField established for
// binding config in task 2.3, generalized here to also cover connection-
// level fields (base_url, credential, and any integration-specific
// connection settings).
type Field struct {
	Key        string
	Label      string
	Type       string // "text" | "select" | "multiselect" | "number" | "boolean" | "secret"
	Required   bool
	RequiredIf map[string]string
	Options    []string
	Default    string
}

// MetricKeyDecl declares one metric a Kind typically emits — feeds task
// 3.7's metric catalog with a friendly label/unit/direction. Not a
// whitelist: a Fact with an undeclared key is still stored and still
// queryable, just without a friendly label until someone adds one here.
type MetricKeyDecl struct {
	Key       string
	Label     string
	Unit      string
	ValueType string // "number" | "text"
	Direction string // "higher_better" | "lower_better" | "neutral"
}

// Descriptor is a Kind's static metadata — everything about it that
// doesn't require a live credential to know.
type Descriptor struct {
	Kind             Kind
	ConnectionFields []Field
	BindingFields    []Field
	BadgeStyle       string
	DefaultInterval  time.Duration
	MinInterval      time.Duration // floor an organization's own interval can't go below
	CostHint         int           // approx. upstream HTTP calls per Collect, for scheduler budgeting
	MetricKeys       []MetricKeyDecl
}

// Session is a live, credential-bound handle to one connection — opened
// once per scheduler batch (task 3.2), not once per binding, specifically
// so a collector can memoize anything shared across its bindings (task
// 3.5's Sentry project-metadata cache is the case this exists for).
type Session interface {
	Ping(ctx context.Context) error                                     // task 2.2's Test Connection
	Discover(ctx context.Context, q DiscoverQuery) ([]Candidate, error) // task 2.4's Discover
	Validate(ctx context.Context, bc BindingConfig) (Resolved, error)   // task 2.3's Test Binding
	Collect(ctx context.Context, req CollectRequest) (Result, error)
	Close() error
}

// Collector is the pull-direction integration contract. Open validates
// the credential structurally (well-formed, not necessarily live-checked
// — Ping is what actually calls upstream) and returns a Session scoped to
// that one connection's lifetime.
type Collector interface {
	Descriptor() Descriptor
	Open(ctx context.Context, conn ConnConfig) (Session, error)
}

// IngestMeta is what the push direction (task 3.4's /ingest/trivy) knows
// about the request that a pull Collect gets for free from CollectRequest:
// which org/component/binding a service-token-authenticated push is
// scoped to.
type IngestMeta struct {
	OrgID       uuid.UUID
	ComponentID uuid.UUID
	BindingID   uuid.UUID
}

// Ingestor is the push-direction integration contract — Parse turns a
// raw payload into the same Result a Collector's Collect would produce,
// so everything below this layer (storage, Headline extraction, the
// dashboard) is identical for both directions.
type Ingestor interface {
	Kind() Kind
	Parse(ctx context.Context, payload []byte, meta IngestMeta) (Result, error)
}
