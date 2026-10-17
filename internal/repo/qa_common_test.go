package repo

import "testing"

func TestQASteps_RoundTripAndNeverNull(t *testing.T) {
	t.Parallel()
	var empty QASteps
	v, err := empty.Value()
	if err != nil || string(v.([]byte)) != "[]" {
		t.Fatalf("empty Value = %s, %v; want []", v, err)
	}
	in := QASteps{{Action: "Buka <b>login</b> — \"é\"", Expected: "Halaman 🚀"}}
	v, _ = in.Value()
	var out QASteps
	if err := out.Scan(v); err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0] != in[0] {
		t.Fatalf("round trip = %#v; want %#v", out, in)
	}
	if err := out.Scan(nil); err != nil || out == nil || len(out) != 0 {
		t.Fatalf("Scan(nil) = %#v, %v; want empty non-nil", out, err)
	}
}

func TestNormalizeQAName(t *testing.T) {
	t.Parallel()
	if got, err := normalizeQAName("  Login  ", MaxQANameLength); err != nil || got != "Login" {
		t.Fatalf("got %q, %v", got, err)
	}
	if _, err := normalizeQAName("   ", MaxQANameLength); err != ErrInvalidInput {
		t.Fatalf("blank: %v; want ErrInvalidInput", err)
	}
	long := make([]rune, MaxQANameLength+1)
	for i := range long {
		long[i] = 'é'
	}
	if _, err := normalizeQAName(string(long), MaxQANameLength); err != ErrInvalidInput {
		t.Fatalf("too long: %v; want ErrInvalidInput", err)
	}
}
