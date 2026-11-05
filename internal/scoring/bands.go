package scoring

import "math"

// The one reading of a 0–100 score, shared by every level (a kind on a
// component, a component, a project) and every page that draws one.
//
// Two bands and an absence, deliberately — the same shape as
// collector.Status after the warn tier was dropped (6d936d0): a reader
// either has something to act on or does not. The colour and the word
// are derived from the number and nothing else, so a score and its
// verdict can never disagree; a kind with a hard failure (a critical CVE,
// a failed pipeline) expresses it by capping its score below
// PassThreshold, not by overriding the colour.

// PassThreshold is the lowest score that reads as healthy.
const PassThreshold = 80

// Tone names a band the way the stylesheet does: pass is green, fail is
// red, none is grey.
type Tone string

const (
	TonePass Tone = "pass"
	ToneFail Tone = "fail"
	ToneNone Tone = "none"
)

// The bands' words.
const (
	WordHealthy        = "Healthy"
	WordNeedsAttention = "Needs attention"
)

// Why a kind has no score. Each is shown beside a grey "–".
const (
	ReasonNotSetUp       = "Not set up"
	ReasonNoData         = "No data"
	ReasonNoSignal       = "No signal"
	ReasonNotScanned     = "Not scanned"
	ReasonNoPipelineData = "No pipeline data"
)

// BandInfo is a score's band: its tone and its word.
type BandInfo struct {
	Tone Tone
	Word string
}

// Band reads a score that exists.
func Band(score int) BandInfo {
	if Passes(score) {
		return BandInfo{Tone: TonePass, Word: WordHealthy}
	}
	return BandInfo{Tone: ToneFail, Word: WordNeedsAttention}
}

// BandOf reads a score that may be absent; reason is the word shown when
// it is (ReasonNoData when empty).
func BandOf(score *int, reason string) BandInfo {
	if score == nil {
		if reason == "" {
			reason = ReasonNoData
		}
		return BandInfo{Tone: ToneNone, Word: reason}
	}
	return Band(*score)
}

// Passes is whether score is in the healthy band.
func Passes(score int) bool { return score >= PassThreshold }

// RoundHalfUp rounds to the nearest whole number, halves up: 79.5 is 80,
// 79.49 is 79. Every level rounds exactly once, with this, at the end —
// never floor, never truncate, never round an intermediate.
func RoundHalfUp(v float64) int {
	return int(math.Floor(v + 0.5))
}

// Cap limits score to at most limit.
func Cap(score, limit int) int {
	if score > limit {
		return limit
	}
	return score
}

// Average is the mean of the scores present, nil when none is: an absent
// score is left out of the average, never counted as zero. It is not
// rounded; the caller rounds once, at its own level.
func Average(scores []*int) *float64 {
	values := make([]*float64, 0, len(scores))
	for _, s := range scores {
		if s != nil {
			v := float64(*s)
			values = append(values, &v)
		}
	}
	return AverageFloat(values)
}

// AverageFloat is Average over unrounded inputs (a project averages its
// per-kind averages before rounding once).
func AverageFloat(values []*float64) *float64 {
	sum, n := 0.0, 0
	for _, v := range values {
		if v != nil {
			sum += *v
			n++
		}
	}
	if n == 0 {
		return nil
	}
	avg := sum / float64(n)
	return &avg
}

// Rounded is RoundHalfUp of a value that may be absent.
func Rounded(v *float64) *int {
	if v == nil {
		return nil
	}
	r := RoundHalfUp(*v)
	return &r
}
