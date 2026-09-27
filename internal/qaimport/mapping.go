// Package qaimport imports a legacy QA Tools installation into one Lotus
// organization (spec: docs/superpowers/specs/2026-09-27-qa-module-design.md §7).
package qaimport

import (
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

func MapItemStatus(legacy string) string {
	switch strings.ToUpper(legacy) {
	case "PASSED":
		return "passed"
	case "FAILED":
		return "failed"
	case "BLOCKED", "HOLD":
		return "blocked"
	case "NOT_APPLICABLE":
		return "skipped"
	default:
		return "untested"
	}
}

func MapResultStatus(legacy string) (string, bool) {
	s := MapItemStatus(legacy)
	return s, s != "untested"
}

func MapIssueStatus(legacy string) string {
	switch strings.ToUpper(legacy) {
	case "OPEN":
		return "open"
	case "FIXED":
		return "fixed"
	case "CLOSED":
		return "closed"
	case "INVALID":
		return "invalid"
	case "DUPLICATE":
		return "duplicate"
	case "REOPEN":
		return "reopened"
	default:
		return "new"
	}
}

func MapSeverity(severity, priority string) string {
	if strings.EqualFold(priority, "SHOW_STOPPER") {
		return "blocker"
	}
	switch strings.ToUpper(severity) {
	case "CRITICAL":
		return "critical"
	case "MINOR":
		return "minor"
	case "TRIVIAL":
		return "trivial"
	default:
		return "major"
	}
}

func MapRole(userType string) string {
	switch strings.ToUpper(userType) {
	case "ADMIN":
		return "admin"
	case "EXTERNAL":
		return "guest"
	default:
		return "member"
	}
}

func RoleRank(role string) int {
	switch role {
	case "admin":
		return 3
	case "member":
		return 2
	case "guest":
		return 1
	}
	return 0
}

var (
	reUAT        = regexp.MustCompile(`(?i)\buat\b|\[uat\]`)
	reSIT        = regexp.MustCompile(`(?i)\bsit\b`)
	reRegression = regexp.MustCompile(`(?i)regres|regrei`)
	reSmoke      = regexp.MustCompile(`(?i)\bsmoke\b|\bsanity\b`)
)

// GuessPhase reads names in order and returns the first phase any of them names.
func GuessPhase(names ...string) string {
	for _, n := range names {
		switch {
		case reUAT.MatchString(n):
			return "uat"
		case reRegression.MatchString(n):
			return "regression"
		case reSIT.MatchString(n):
			return "sit"
		case reSmoke.MatchString(n):
			return "smoke"
		}
	}
	return "other"
}

func CleanText(s string) string {
	s = strings.TrimSpace(s)
	if s == "-" {
		return ""
	}
	return s
}

// CasePriority drops CRITICAL: it was the legacy default (65% of cases), so a
// CRITICAL row cannot be told apart from one nobody prioritised.
func CasePriority(p string) *string {
	var v string
	switch strings.ToUpper(p) {
	case "HIGH":
		v = "high"
	case "MEDIUM":
		v = "medium"
	case "LOW":
		v = "low"
	default:
		return nil
	}
	return &v
}

func RunNotes(description, version, browser string) string {
	var parts []string
	if d := CleanText(description); d != "" {
		parts = append(parts, d)
	}
	if v := CleanText(version); v != "" {
		parts = append(parts, "Build: "+v)
	}
	if b := CleanText(browser); b != "" {
		parts = append(parts, "Browser: "+b)
	}
	return strings.Join(parts, "\n")
}

var reTrailingDigits = regexp.MustCompile(`(\d+)\s*$`)

// AllocateKeys turns legacy codes (TC0001, D0012) into per-project integer
// keys, in input order. The first row carrying a valid, unused number keeps
// it; duplicates and rows without a number get max+1, max+2, … .
func AllocateKeys(codes []string) []int {
	parsed := make([]int, len(codes))
	used := map[int]bool{}
	maxKey := 0
	for i, c := range codes {
		m := reTrailingDigits.FindStringSubmatch(c)
		if m == nil {
			continue
		}
		n, err := strconv.Atoi(m[1])
		if err != nil || n <= 0 || used[n] {
			continue
		}
		used[n] = true
		parsed[i] = n
		if n > maxKey {
			maxKey = n
		}
	}
	for i := range parsed {
		if parsed[i] == 0 {
			maxKey++
			parsed[i] = maxKey
		}
	}
	return parsed
}

// WithLegacyCode keeps a renumbered row's old identifier findable: teams
// quote codes like TC-LIST-01 in chats and tickets.
func WithLegacyCode(text, code string) string {
	return strings.TrimSpace("Legacy code: " + code + "\n\n" + text)
}

var reNonSlug = regexp.MustCompile(`[^a-z0-9]+`)

func Slugify(title string) string {
	s := strings.Trim(reNonSlug.ReplaceAllString(strings.ToLower(title), "-"), "-")
	if len(s) > 60 {
		s = strings.Trim(s[:60], "-")
	}
	if s == "" {
		return "project"
	}
	return s
}

// TruncateRunes shortens s to at most max characters, ending with "…" when cut.
func TruncateRunes(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	r := []rune(s)
	return string(r[:max-1]) + "…"
}
