package sonarqube

import (
	"testing"

	"github.com/uyab/lotus/internal/collector"
	"github.com/uyab/lotus/internal/repo"
)

// A stored snapshot rebuilds the headline a collect would write today:
// from raw_json when it has one, from its columns when it does not.
func TestHeadlineFromSnapshot(t *testing.T) {
	str := func(v string) *string { return &v }
	f := func(v float64) *float64 { return &v }
	fromRaw, ok := HeadlineFromSnapshot(&repo.SonarSnapshot{RawJSON: []byte(`{"QualityGate":"ERROR","Coverage":90,"ReliabilityRating":"A","SecurityRating":"A","SqaleRating":"A","Bugs":0}`)})
	if !ok || fromRaw.Score == nil || *fromRaw.Score != 48 || fromRaw.Status != collector.StatusFail || fromRaw.Grade != "A" {
		t.Fatalf("from raw_json = %+v", fromRaw)
	}
	fromCols, ok := HeadlineFromSnapshot(&repo.SonarSnapshot{QualityGate: str("OK"), Coverage: f(95.5), ReliabilityRating: str("A")})
	if !ok || fromCols.Score == nil || *fromCols.Score != 98 || fromCols.Status != collector.StatusPass {
		t.Fatalf("from columns = %+v", fromCols)
	}
	if _, ok := HeadlineFromSnapshot(&repo.SonarSnapshot{}); ok {
		t.Fatal("an empty snapshot has nothing to rebuild from")
	}
}
