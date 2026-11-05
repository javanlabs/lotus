package main

import (
	"net/url"
	"testing"
)

// A dry run's connection makes every transaction read-only, keeping the
// DSN's own parameters.
func TestReadOnlyDSN(t *testing.T) {
	got, err := readOnlyDSN("postgres://postgres@localhost:5418/lotus?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(got)
	if err != nil {
		t.Fatal(err)
	}
	if q := u.Query(); q.Get("default_transaction_read_only") != "on" || q.Get("sslmode") != "disable" || u.Path != "/lotus" {
		t.Fatalf("dsn = %s", got)
	}
}
