package authz

import (
	"reflect"
	"sync"

	"github.com/gofiber/fiber/v2"
)

// Task 1.5's route-coverage test needs to answer, for an arbitrary
// registered fiber.Handler, "was this produced by Require or Public" —
// without every route-registration call site having to say so a second
// time in some separate table that could drift from the actual routes.
// Reflect's Pointer() gives the code address of a closure, which Go
// guarantees is stable across separate invocations of the same closure
// literal regardless of what it captured — exactly the "was this handler
// one of ours" check this needs, and the only thing it needs: it does not
// need to recover which specific permission or reason a given closure
// instance captured, since the coverage test only asks "is there a
// decision here" and, separately, prints each route's own known
// method+path when logging the public ones.
var (
	guardMu     sync.Mutex
	requirePtrs = map[uintptr]bool{}
	publicPtrs  = map[uintptr]bool{}
)

// RouteDecision reports whether h is a handler produced by Require
// ("require") or Public ("public"), for the task 1.5 coverage test.
func RouteDecision(h fiber.Handler) (kind string, ok bool) {
	ptr := reflect.ValueOf(h).Pointer()
	guardMu.Lock()
	defer guardMu.Unlock()
	if requirePtrs[ptr] {
		return "require", true
	}
	if publicPtrs[ptr] {
		return "public", true
	}
	return "", false
}
