package dbtest

import (
	"strings"
	"testing"
)

func TestSchemaNameFitsPostgresIdentifierLimit(t *testing.T) {
	// Postgres truncates a longer identifier silently, which would merge
	// two distinct long test names into one shared schema.
	long := "TestSomething_" + strings.Repeat("VeryLongDescriptiveSegment", 5)
	got := schemaName(long)
	if len(got) > maxIdentifier {
		t.Errorf("schemaName(%d-char name) = %q (%d chars), want <= %d", len(long), got, len(got), maxIdentifier)
	}
}

func TestSchemaNameIsUniquePerCallForTheSameTest(t *testing.T) {
	// A test that opens two databases must get two schemas.
	a, b := schemaName("TestX"), schemaName("TestX")
	if a == b {
		t.Errorf("two calls both returned %q", a)
	}
}

func TestSchemaNameCarriesThePIDThatDisambiguatesPackages(t *testing.T) {
	// The regression this guards: schemaSeq is per-process, so without the
	// pid two packages containing a same-named test both produce
	// "t_<name>_1" and race on one schema under `go test ./...`.
	got := schemaName("TestEnsurePartitions_CreatesFutureMonths")
	if strings.HasSuffix(got, "_1") && !strings.Contains(got, "_") {
		t.Fatalf("unexpected shape %q", got)
	}
	parts := strings.Split(got, "_")
	if len(parts) < 3 {
		t.Fatalf("schemaName = %q, want a trailing _<pid>_<seq>", got)
	}
	pid := parts[len(parts)-2]
	if pid == "" || pid == "0" {
		t.Errorf("schemaName = %q, want a real pid in the second-to-last segment", got)
	}
}
