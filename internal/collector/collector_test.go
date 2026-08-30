package collector_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/uyab/lotus/internal/collector"
)

// mockCollector/mockSession exist only for this package's own tests —
// task 3.1 says nothing about implementing a real integration, only that
// the shape works for one. The real GitLab/SonarQube/Trivy/Sentry
// collectors (tasks 3.3-3.5) are what actually exercise this interface
// end to end.
type mockCollector struct {
	desc      collector.Descriptor
	openErr   error
	collectFn func(collector.CollectRequest) (collector.Result, error)
}

func (m mockCollector) Descriptor() collector.Descriptor { return m.desc }
func (m mockCollector) Open(ctx context.Context, conn collector.ConnConfig) (collector.Session, error) {
	if m.openErr != nil {
		return nil, m.openErr
	}
	return mockSession{m}, nil
}

type mockSession struct{ c mockCollector }

func (s mockSession) Ping(ctx context.Context) error { return nil }
func (s mockSession) Discover(ctx context.Context, q collector.DiscoverQuery) ([]collector.Candidate, error) {
	return nil, nil
}
func (s mockSession) Validate(ctx context.Context, bc collector.BindingConfig) (collector.Resolved, error) {
	return collector.Resolved{OK: true}, nil
}
func (s mockSession) Collect(ctx context.Context, req collector.CollectRequest) (collector.Result, error) {
	return s.c.collectFn(req)
}
func (s mockSession) Close() error { return nil }

func TestRegistry_RegisterAndGet(t *testing.T) {
	k := collector.Kind("mock-registry-test")
	m := mockCollector{desc: collector.Descriptor{Kind: k}}
	collector.Register(k, m)

	got, ok := collector.Get(k)
	if !ok {
		t.Fatal("expected the registered collector to be found")
	}
	if got.Descriptor().Kind != k {
		t.Fatalf("got kind %q, want %q", got.Descriptor().Kind, k)
	}

	found := false
	for _, r := range collector.Registered() {
		if r == k {
			found = true
		}
	}
	if !found {
		t.Fatal("Registered() should include the newly registered kind")
	}
}

func TestRegistry_DuplicateRegistrationPanics(t *testing.T) {
	k := collector.Kind("mock-duplicate-test")
	collector.Register(k, mockCollector{desc: collector.Descriptor{Kind: k}})

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected a duplicate registration to panic")
		}
	}()
	collector.Register(k, mockCollector{desc: collector.Descriptor{Kind: k}})
}

func TestClassOf_AuthErrorIsClassifiedAndNeverRetried(t *testing.T) {
	err := collector.ErrAuth(errors.New("401 unauthorized"))
	if collector.ClassOf(err) != collector.ErrClassAuth {
		t.Fatalf("ClassOf = %q, want auth", collector.ClassOf(err))
	}
	// A plain, un-classified error must not silently look retryable —
	// see ClassOf's own doc comment: defaults to the least-retryable,
	// most-visible class rather than a retry loop.
	if collector.ClassOf(errors.New("some random error")) != collector.ErrClassInternal {
		t.Fatal("an un-classified error should default to ErrClassInternal, not fall through to a retryable class")
	}
}

func TestClassOf_RateLimitedCarriesRetryAfter(t *testing.T) {
	err := collector.ErrRateLimited(errors.New("429"), 90*time.Second)
	if collector.ClassOf(err) != collector.ErrClassRateLimited {
		t.Fatalf("ClassOf = %q, want rate_limited", collector.ClassOf(err))
	}
	if got := collector.RetryAfterOf(err); got != 90*time.Second {
		t.Fatalf("RetryAfterOf = %v, want 90s", got)
	}
}

// TestPartialResultIsNotAFailure demonstrates task 3.1's core rule at the
// type level: a Result with non-empty Warnings and non-empty Facts is
// still valid (ValidateResult passes) — the scheduler (task 3.2) is what
// turns "Warnings present" into a run status of "partial" rather than
// "failed"; this test only proves the Result itself doesn't reject that
// shape.
func TestPartialResultIsNotAFailure(t *testing.T) {
	v := 1.0
	r := collector.Result{
		Headline: collector.Headline{Status: collector.StatusFail},
		Facts:    []collector.Fact{{Key: "gitlab.commits_30d", ValueNum: &v}},
		Warnings: []collector.Warning{{Step: "languages", Err: "404"}},
	}
	if err := collector.ValidateResult(r); err != nil {
		t.Fatalf("a Result with Warnings and non-empty Facts must still validate: %v", err)
	}
}

func TestValidateResult_RejectsEmptyFactsOnALeafResult(t *testing.T) {
	r := collector.Result{Headline: collector.Headline{Status: collector.StatusPass}}
	if err := collector.ValidateResult(r); err == nil {
		t.Fatal("expected a leaf Result with zero Facts to be rejected as a collector bug")
	}
}

func TestValidateResult_ChildrenCarryFactsInsteadOfParent(t *testing.T) {
	v := 5.0
	r := collector.Result{
		Headline: collector.Headline{Status: collector.StatusPass},
		// No Facts on the parent — Sentry's per-environment fan-out shape.
		Children: []collector.Result{
			{Headline: collector.Headline{Status: collector.StatusPass}, Facts: []collector.Fact{{Key: "sentry.issues_unresolved", ValueNum: &v}}},
			{Headline: collector.Headline{Status: collector.StatusPass}, Facts: []collector.Fact{{Key: "sentry.issues_unresolved", ValueNum: &v}}},
		},
	}
	if err := collector.ValidateResult(r); err != nil {
		t.Fatalf("a parent Result with Facts on its Children should validate: %v", err)
	}
}

func TestValidateResult_RejectsAChildWithNoFacts(t *testing.T) {
	r := collector.Result{
		Children: []collector.Result{
			{Headline: collector.Headline{Status: collector.StatusPass}}, // no Facts
		},
	}
	if err := collector.ValidateResult(r); err == nil {
		t.Fatal("expected a Child with zero Facts to be rejected")
	}
}

// TestFailedSubCallOmitsFactRatherThanZeroingIt demonstrates the pattern
// real collectors (task 3.3+) must follow: a failed sub-call removes the
// corresponding Fact and adds a Warning, it never writes a zero-value
// Fact in its place. This mock simulates that choice directly (there's no
// real collector yet in this package) to prove the Result shape supports
// it cleanly.
func TestFailedSubCallOmitsFactRatherThanZeroingIt(t *testing.T) {
	collect := func(languagesCallFails bool) collector.Result {
		commits := 12.0
		facts := []collector.Fact{{Key: "gitlab.commits_30d", ValueNum: &commits}}
		var warnings []collector.Warning
		if languagesCallFails {
			warnings = append(warnings, collector.Warning{Step: "languages", Err: "upstream 500"})
			// Deliberately NOT appending a gitlab.languages_count fact —
			// this is the behavior under test.
		} else {
			langs := 3.0
			facts = append(facts, collector.Fact{Key: "gitlab.languages_count", ValueNum: &langs})
		}
		return collector.Result{Headline: collector.Headline{Status: collector.StatusPass}, Facts: facts, Warnings: warnings}
	}

	failed := collect(true)
	for _, f := range failed.Facts {
		if f.Key == "gitlab.languages_count" {
			t.Fatal("a failed sub-call must omit its Fact, not write a zero-value one")
		}
	}
	if len(failed.Warnings) != 1 {
		t.Fatalf("expected one Warning for the failed sub-call, got %d", len(failed.Warnings))
	}

	ok := collect(false)
	found := false
	for _, f := range ok.Facts {
		if f.Key == "gitlab.languages_count" {
			found = true
		}
	}
	if !found {
		t.Fatal("a successful sub-call should include its Fact")
	}
}

// TestDescriptor_FormFieldsRenderableWithoutKindSpecificCode proves
// Descriptor's Field data is generic enough that rendering a form needs
// no per-kind branching — the actual HTML rendering is task 4.3, out of
// scope here; this exercises the same generic-enumeration property a
// template would rely on.
func TestDescriptor_FormFieldsRenderableWithoutKindSpecificCode(t *testing.T) {
	desc := collector.Descriptor{
		Kind: "mock-form-test",
		ConnectionFields: []collector.Field{
			{Key: "base_url", Label: "Base URL", Type: "text", Required: true},
			{Key: "token", Label: "Token", Type: "secret", Required: true},
		},
		BindingFields: []collector.Field{
			{Key: "mode", Label: "Mode", Type: "select", Options: []string{"shared", "dedicated"}, Required: true},
			{Key: "service_tag_value", Label: "Service tag value", Type: "text", RequiredIf: map[string]string{"mode": "shared"}},
		},
	}

	renderGeneric := func(fields []collector.Field) []string {
		var labels []string
		for _, f := range fields {
			labels = append(labels, f.Label)
		}
		return labels
	}

	connLabels := renderGeneric(desc.ConnectionFields)
	if len(connLabels) != 2 || connLabels[0] != "Base URL" {
		t.Fatalf("connection field labels = %v", connLabels)
	}
	bindingLabels := renderGeneric(desc.BindingFields)
	if len(bindingLabels) != 2 || bindingLabels[1] != "Service tag value" {
		t.Fatalf("binding field labels = %v", bindingLabels)
	}
}

func TestMetricKeys_NotAWhitelist(t *testing.T) {
	declared := []collector.MetricKeyDecl{
		{Key: "gitlab.commits_30d", Label: "Commits (30d)", Unit: "count", ValueType: "number", Direction: "higher_better"},
	}
	// A Fact using a key NOT in MetricKeys must still be a perfectly
	// valid Fact — the declaration is metadata for the catalog (task
	// 3.7), not a filter on what Collect is allowed to emit.
	v := 1.0
	undeclaredFact := collector.Fact{Key: "gitlab.some_new_metric_nobody_declared_yet", ValueNum: &v}
	r := collector.Result{Headline: collector.Headline{Status: collector.StatusPass}, Facts: []collector.Fact{undeclaredFact}}
	if err := collector.ValidateResult(r); err != nil {
		t.Fatalf("an undeclared Fact key must still be valid: %v", err)
	}
	if len(declared) != 1 {
		t.Fatalf("sanity: expected exactly one declared metric key in this fixture, got %d", len(declared))
	}
}
