#!/usr/bin/env bash
# Task 0.4 guardrail: internal/repo (and its internal/repo/global exception)
# are the only packages allowed to call db.Get/db.Select/db.Exec/db.Query
# directly. Everything else — handlers above all — must go through a
# Scope-accepting repo function instead of touching the database itself.
#
# This is a textual grep, not an AST check, because Go's type system can't
# express "only these two packages may hold a raw connection" — see
# internal/repo/doc.go. It only catches the literal `db.<Method>(` spelling
# used throughout this codebase's repo layer; a deliberately obfuscated call
# would slip through, but that's not the threat model here.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root"

pattern='\bdb\.(Get|Select|Exec|Query)\('
violations=$(grep -rnE "$pattern" \
  --include='*.go' \
  --exclude='*_test.go' \
  cmd internal web 2>/dev/null \
  | grep -v '^internal/repo/' \
  | grep -v '^internal/db/' \
  | grep -v '^internal/dbtest/' \
  || true)

if [ -n "$violations" ]; then
  echo "Raw db.Get/db.Select/db.Exec/db.Query calls found outside internal/repo:" >&2
  echo "$violations" >&2
  echo >&2
  echo "Route this query through a tenant.Scope-accepting function in internal/repo" >&2
  echo "(or internal/repo/global for control-plane tables) instead." >&2
  exit 1
fi

echo "check-repo-scope: ok"
