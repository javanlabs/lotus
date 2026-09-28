package sonarqube

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/clients/sonar"
	"github.com/uyab/lotus/internal/collector"
	"github.com/uyab/lotus/internal/repo"
	"github.com/uyab/lotus/internal/tenant"
)

// buildResult translates a Measures snapshot into task 3.1's Result
// shape. Unlike GitLab's per-step Warnings, SonarQube's Fetch has exactly
// one fatal call (measures) and two best-effort ones (quality gate, last
// scan) that the ported client already silently no-ops on failure —
// there's nothing here to selectively omit a Fact for, since a fact whose
// measure genuinely wasn't returned by the API is already nil (parseInt/
// parseFloat return nil on a missing/unparseable value) and is skipped
// the same way any other unset measure is.
func buildResult(m *sonar.Measures) (collector.Result, error) {
	var facts []collector.Fact
	numFact := func(key string, v *float64, unit string) {
		if v == nil {
			return
		}
		vv := *v
		facts = append(facts, collector.Fact{Key: key, ValueNum: &vv, Unit: unit})
	}
	intFact := func(key string, v *int, unit string) {
		if v == nil {
			return
		}
		vv := float64(*v)
		facts = append(facts, collector.Fact{Key: key, ValueNum: &vv, Unit: unit})
	}
	textFact := func(key, v, unit string) {
		if v == "" {
			return
		}
		vv := v
		facts = append(facts, collector.Fact{Key: key, ValueText: &vv, Unit: unit})
	}

	numFact("sonar.coverage", m.Coverage, "%")
	intFact("sonar.bugs", m.Bugs, "count")
	intFact("sonar.vulnerabilities", m.Vulnerabilities, "count")
	intFact("sonar.code_smells", m.CodeSmells, "count")
	intFact("sonar.security_hotspots", m.SecurityHotspots, "count")
	numFact("sonar.duplicated_lines_density", m.DuplicatedLinesDensity, "%")
	intFact("sonar.ncloc", m.NCLOC, "count")
	intFact("sonar.complexity", m.Complexity, "count")
	intFact("sonar.sqale_index_min", m.SqaleIndexMin, "minutes")
	intFact("sonar.test_execution_time_ms", m.TestExecutionTimeMs, "ms")
	textFact("sonar.reliability_rating", m.ReliabilityRating, "grade")
	textFact("sonar.security_rating", m.SecurityRating, "grade")
	textFact("sonar.sqale_rating", m.SqaleRating, "grade")

	if len(facts) == 0 {
		return collector.Result{}, collector.ErrUpstream(fmt.Errorf("sonarqube: no measures returned"))
	}

	status := collector.StatusUnknown
	switch m.QualityGate {
	case "OK":
		status = collector.StatusPass
	case "WARN":
		status = collector.StatusFail
	case "ERROR":
		status = collector.StatusFail
	}

	worst := m.WorstRating()

	// Score is a base blended from coverage and the worst rating, then
	// multiplied by a quality-gate factor:
	//
	//   base = average of { coverage %, worst rating (A=100 .. E=20) },
	//          skipping whichever is unmeasured — never counting it as 0;
	//   score = base x 1.0 (gate passed) / x 0.75 (warn) / x 0.5 (failed).
	//
	// The gate is a MULTIPLIER rather than one more term in the average on
	// purpose: it is the pass/fail verdict the team itself configured, so
	// it must dominate. Averaged in, a failing gate was still rescued by
	// good coverage (0+90+100 over 3 read 63 — "decent"); as a factor, 89%
	// coverage with a failing gate reads 44, and no amount of coverage can
	// push a failing project above 50. A project reporting neither
	// coverage nor ratings gets no score at all — the gate alone still
	// drives the colour, but a bare multiplier has nothing to multiply.
	var parts []int
	if m.Coverage != nil {
		parts = append(parts, int(*m.Coverage))
	}
	if v, ok := map[string]int{"A": 100, "B": 80, "C": 60, "D": 40, "E": 20}[worst]; ok {
		parts = append(parts, v)
	}
	var score *int
	if len(parts) > 0 {
		sum := 0
		for _, p := range parts {
			sum += p
		}
		base := float64(sum) / float64(len(parts))
		gateFactor := 1.0
		switch m.QualityGate {
		case "WARN":
			gateFactor = 0.75
		case "ERROR":
			gateFactor = 0.5
		}
		v := int(base * gateFactor)
		score = &v
	}

	var countValue *int64
	if m.Bugs != nil && m.Vulnerabilities != nil {
		v := int64(*m.Bugs + *m.Vulnerabilities)
		countValue = &v
	}

	summary := m.QualityGate
	if summary == "" {
		summary = "no quality gate data"
	}

	detail, err := json.Marshal(m)
	if err != nil {
		return collector.Result{}, collector.ErrInternal(err)
	}

	return collector.Result{
		Headline: collector.Headline{
			Status: status, Score: score, Grade: worst,
			CountValue: countValue, CountLabel: "bugs + vulnerabilities", Summary: summary,
		},
		Facts:      facts,
		Detail:     detail,
		DetailKind: string(collector.KindSonarQube),
	}, nil
}

// BuildImportedResult turns measures read from elsewhere — the legacy
// Lotus's sonar_snapshots rows (`lotus import-legacy`) — into the same
// Result a live collect produces, so an import goes through Persist and
// the facts writer exactly like collected data.
func BuildImportedResult(m *sonar.Measures) (collector.Result, error) {
	return buildResult(m)
}

// Persist writes a Collect's Detail into sonar_snapshots.
func Persist(ctx context.Context, s tenant.Scope, componentID, bindingID uuid.UUID, r collector.Result) error {
	var m sonar.Measures
	if err := json.Unmarshal(r.Detail, &m); err != nil {
		return err
	}
	_, _, err := repo.InsertSonarSnapshot(ctx, s, repo.NewSonarSnapshot{
		BindingID: bindingID, ComponentID: componentID,
		QualityGate:            strPtr(m.QualityGate),
		Coverage:               m.Coverage,
		Bugs:                   m.Bugs,
		Vulnerabilities:        m.Vulnerabilities,
		CodeSmells:             m.CodeSmells,
		SecurityHotspots:       m.SecurityHotspots,
		DuplicatedLinesDensity: m.DuplicatedLinesDensity,
		NCLOC:                  m.NCLOC,
		Complexity:             m.Complexity,
		ReliabilityRating:      strPtr(m.ReliabilityRating),
		SecurityRating:         strPtr(m.SecurityRating),
		SqaleRating:            strPtr(m.SqaleRating),
		SqaleIndexMin:          m.SqaleIndexMin,
		TestExecutionTimeMs:    m.TestExecutionTimeMs,
		Languages:              strPtr(m.Languages),
		LastScanAt:             m.LastScan,
		RawJSON:                r.Detail,
	}, collector.ContentHash(r), r.Headline)
	return err
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
