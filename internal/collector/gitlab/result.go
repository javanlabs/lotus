package gitlab

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"

	glclient "github.com/uyab/lotus/internal/clients/gitlab"
	"github.com/uyab/lotus/internal/collector"
	"github.com/uyab/lotus/internal/repo"
	"github.com/uyab/lotus/internal/tenant"
)

// buildResult translates a Health snapshot into task 3.1's Result shape.
// Facts and their source Warnings are aligned 1:1 with FetchHealth's own
// best-effort sub-calls (languages/branches/pipelines/commits): a step
// that failed adds a Warning and contributes no Fact for the data it
// would have produced — task 3.1's most-violated rule, enforced here by
// construction (there's no path that both records a warning for a step
// and still emits that step's facts).
func buildResult(h *glclient.Health) (collector.Result, error) {
	failed := map[string]bool{}
	var warnings []collector.Warning
	for _, w := range h.Warnings {
		failed[w.Step] = true
		warnings = append(warnings, collector.Warning{Step: w.Step, Err: w.Err})
	}

	var facts []collector.Fact
	numFact := func(key string, v float64, unit string) {
		vv := v
		facts = append(facts, collector.Fact{Key: key, ValueNum: &vv, Unit: unit})
	}

	if !failed["pipelines"] && h.PipelineSuccessRate != nil {
		numFact("gitlab.pipeline_success_rate", *h.PipelineSuccessRate*100, "%")
		numFact("gitlab.pipeline_sample_size", float64(h.PipelineSampleSize), "count")
	}
	if !failed["branches"] {
		numFact("gitlab.branches_total", float64(h.BranchesTotal), "count")
		numFact("gitlab.branches_active", float64(h.BranchesActive), "count")
		numFact("gitlab.branches_stale", float64(h.BranchesStale), "count")
	}
	if !failed["commits"] {
		numFact("gitlab.commits_30d", float64(h.Commits30d), "count")
	}

	// task 3.1: "tiap collect yang sukses wajib ngasilin minimal satu
	// fact" — if every single sub-call failed (pathological, but
	// possible), there is nothing left to report as a Fact at all. That's
	// a real collector-level failure, not a partial success, so it's
	// surfaced as an upstream error rather than an empty-Facts Result
	// (which ValidateResult would reject anyway, but as an *internal*
	// bug rather than the *upstream* problem it actually is).
	if len(facts) == 0 {
		return collector.Result{}, collector.ErrUpstream(fmt.Errorf("gitlab: every sub-call failed, nothing to report"))
	}

	status := collector.StatusUnknown
	switch h.LastPipelineStatus {
	case "success":
		status = collector.StatusPass
	case "failed":
		status = collector.StatusFail
	case "running", "pending":
		status = collector.StatusWarn
	}

	var score *int
	if h.PipelineSuccessRate != nil {
		v := int(*h.PipelineSuccessRate * 100)
		score = &v
	}

	var countValue *int64
	if !failed["branches"] {
		v := int64(h.BranchesStale)
		countValue = &v
	}

	summary := h.LastPipelineStatus
	if summary == "" {
		summary = "no pipeline data"
	}

	detail, err := json.Marshal(h)
	if err != nil {
		return collector.Result{}, collector.ErrInternal(err)
	}

	return collector.Result{
		Headline: collector.Headline{
			Status: status, Score: score, CountValue: countValue, CountLabel: "stale branch",
			Summary: summary, DeepLink: h.WebURL,
		},
		Facts:      facts,
		Detail:     detail,
		DetailKind: string(collector.KindGitLab),
		Warnings:   warnings,
	}, nil
}

// Persist writes a Collect's Detail into gitlab_snapshots — see
// internal/collector.PersistFunc's own doc comment for why this is
// registered rather than called directly by the scheduler.
//
// Fields belonging to a sub-call that failed (recorded in h.Warnings) are
// written as SQL NULL, not as Go's int/string zero value — the same
// "omit, don't zero" rule buildResult applies to Facts applies here too;
// a branches-call failure must not read back as "0 branches".
func Persist(ctx context.Context, s tenant.Scope, componentID, bindingID uuid.UUID, r collector.Result) error {
	var h glclient.Health
	if err := json.Unmarshal(r.Detail, &h); err != nil {
		return err
	}

	failed := map[string]bool{}
	for _, w := range h.Warnings {
		failed[w.Step] = true
	}

	in := repo.NewGitlabSnapshot{
		BindingID: bindingID, ComponentID: componentID,

		DefaultBranch:          strPtr(h.DefaultBranch),
		Visibility:             strPtr(h.Visibility),
		Archived:               boolPtr(h.Archived),
		WebURL:                 strPtr(h.WebURL),
		LastActivityAt:         h.LastActivityAt,
		DefaultBranchProtected: boolPtr(h.DefaultBranchProtected),

		PrimaryLanguage: strPtr(h.PrimaryLanguage),
		LanguagesJSON:   jsonPtr(h.LanguagesJSON),

		LastPipelineStatus:      strPtr(h.LastPipelineStatus),
		LastPipelineID:          h.LastPipelineID,
		LastPipelineDurationSec: h.LastPipelineDurationSec,
		LastPipelineWebURL:      strPtr(h.LastPipelineWebURL),
		LastPipelineFinishedAt:  h.LastPipelineFinishedAt,
		LastPipelineRef:         strPtr(h.LastPipelineRef),
		LastCommitterName:       strPtr(h.LastCommitterName),

		WarningsJSON: warningsJSON(r.Warnings),
		RawJSON:      r.Detail,
	}

	if !failed["pipelines"] {
		in.PipelineSuccessRate = h.PipelineSuccessRate
		in.PipelineSampleSize = &h.PipelineSampleSize
	}
	if !failed["branches"] {
		in.BranchesTotal = &h.BranchesTotal
		in.BranchesActive = &h.BranchesActive
		in.BranchesStale = &h.BranchesStale
	}
	if !failed["commits"] {
		in.Commits30d = &h.Commits30d
		in.Commits30dCapped = &h.Commits30dCapped
	}

	_, _, err := repo.InsertGitlabSnapshot(ctx, s, in, collector.ContentHash(r), r.Headline)
	return err
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func boolPtr(b bool) *bool { return &b }

func jsonPtr(s string) json.RawMessage {
	if s == "" {
		return nil
	}
	return json.RawMessage(s)
}

func warningsJSON(warnings []collector.Warning) json.RawMessage {
	if len(warnings) == 0 {
		return nil
	}
	b, _ := json.Marshal(warnings)
	return b
}
