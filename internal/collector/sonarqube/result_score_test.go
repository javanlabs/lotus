package sonarqube

import (
	"testing"

	"github.com/uyab/lotus/internal/clients/sonar"
)

func cov(v float64) *float64 { return &v }

// TestScore_GateIsAMultiplierNotATerm pins the shape of the formula: the
// base blends coverage with the worst rating, and the gate scales it.
// The AlurKerja shape from the real Sonar dashboard — gate passed, 95.5%
// coverage, worst rating A — lands at (95+100)/2 x 1.0 = 97.
func TestScore_GateIsAMultiplierNotATerm(t *testing.T) {
	r, err := buildResult(&sonar.Measures{
		QualityGate:       "OK",
		Coverage:          cov(95.5),
		ReliabilityRating: "A", SecurityRating: "A", SqaleRating: "A",
	})
	if err != nil {
		t.Fatalf("buildResult: %v", err)
	}
	if r.Headline.Score == nil || *r.Headline.Score != 97 {
		t.Fatalf("Score = %v, want 97 ((95 coverage + 100 rating A) / 2, gate passed x1.0)", r.Headline.Score)
	}
}

// TestScore_FailingGateHalvesTheScore is the requirement verbatim: 89%
// coverage with a failing gate must be punished hard — 89 x 0.5 = 44 —
// because the gate is the verdict the team itself configured, and good
// coverage must not be able to rescue a failing project. Averaging the
// gate in (the previous formula) would have read 59 here.
func TestScore_FailingGateHalvesTheScore(t *testing.T) {
	r, err := buildResult(&sonar.Measures{
		QualityGate: "ERROR",
		Coverage:    cov(89),
	})
	if err != nil {
		t.Fatalf("buildResult: %v", err)
	}
	if r.Headline.Score == nil || *r.Headline.Score != 44 {
		t.Fatalf("Score = %v, want 44 (89 coverage x 0.5 failing gate)", r.Headline.Score)
	}
}

// TestScore_NoCoverageNoRatingMeansNoScore: the gate alone has nothing to
// multiply. It still drives the colour, but a bare verdict is not a
// number — reporting 100 or 0 for a project with no measurements would be
// inventing data.
func TestScore_NoCoverageNoRatingMeansNoScore(t *testing.T) {
	r, err := buildResult(&sonar.Measures{QualityGate: "OK", Bugs: intp(0)})
	if err != nil {
		t.Fatalf("buildResult: %v", err)
	}
	if r.Headline.Score != nil {
		t.Fatalf("Score = %d, want nil (nothing measurable to score)", *r.Headline.Score)
	}
}

// TestScore_WarnGateScalesByThreeQuarters covers the middle verdict.
// (80 coverage + 80 rating B) / 2 = 80, x 0.75 = 60.
func TestScore_WarnGateScalesByThreeQuarters(t *testing.T) {
	r, err := buildResult(&sonar.Measures{
		QualityGate:       "WARN",
		Coverage:          cov(80),
		ReliabilityRating: "B",
	})
	if err != nil {
		t.Fatalf("buildResult: %v", err)
	}
	if r.Headline.Score == nil || *r.Headline.Score != 60 {
		t.Fatalf("Score = %v, want 60 ((80+80)/2 x 0.75 warn gate)", r.Headline.Score)
	}
}

func intp(v int) *int { return &v }
