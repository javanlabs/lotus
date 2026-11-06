package scoring

import "testing"

func ip(v int) *int { return &v }

func fp(v float64) *float64 { return &v }

func TestBand(t *testing.T) {
	for _, tc := range []struct {
		score int
		want  BandInfo
	}{
		{100, BandInfo{TonePass, WordPass}},
		{80, BandInfo{TonePass, WordPass}},
		{79, BandInfo{ToneFail, WordFail}},
		{0, BandInfo{ToneFail, WordFail}},
	} {
		if got := Band(tc.score); got != tc.want {
			t.Errorf("Band(%d) = %+v, want %+v", tc.score, got, tc.want)
		}
	}
	if got := BandOf(nil, ReasonNotScanned); got != (BandInfo{ToneNone, ReasonNotScanned}) {
		t.Errorf("BandOf(nil) = %+v", got)
	}
	if got := BandOf(nil, ""); got.Word != ReasonNoData {
		t.Errorf("BandOf(nil, \"\") word = %q, want %q", got.Word, ReasonNoData)
	}
}

func TestRoundHalfUp(t *testing.T) {
	for in, want := range map[float64]int{79.5: 80, 79.49: 79, 47.5: 48, 0.5: 1, 0.49: 0, 84: 84, 83.999999: 84} {
		if got := RoundHalfUp(in); got != want {
			t.Errorf("RoundHalfUp(%v) = %d, want %d", in, got, want)
		}
	}
}

func TestCap(t *testing.T) {
	if Cap(95, 79) != 79 || Cap(40, 49) != 40 {
		t.Fatal("Cap did not limit from above only")
	}
}

func TestAverageSkipsMissing(t *testing.T) {
	avg := Average([]*int{ip(90), nil, ip(75)})
	if avg == nil || *avg != 82.5 {
		t.Fatalf("Average = %v, want 82.5", avg)
	}
	if Average([]*int{nil, nil}) != nil {
		t.Fatal("Average of nothing should be nil, not zero")
	}
	if r := Rounded(avg); r == nil || *r != 83 {
		t.Fatalf("Rounded(82.5) = %v, want 83", r)
	}
}
