package collector

import (
	"crypto/sha256"
	"encoding/hex"
)

// ContentHash returns a stable hash of r's Detail (and, if r has
// Children, each child's own Detail bytes in order) — task 3.6's write
// dedupe rule needs to tell "the upstream tool answered with the exact
// same detail as last time" apart from "something changed", and a
// collector's own Detail bytes are already the one thing every kind
// marshals deterministically for exactly this reason (Persist unmarshals
// them straight into a snapshot row). Computed here, generically, once,
// rather than by each collector — the same "generic code doesn't know
// about any one kind" rule this task applies to the dashboard applies
// here too.
func ContentHash(r Result) string {
	h := sha256.New()
	h.Write(r.Detail)
	for _, c := range r.Children {
		h.Write(c.Detail)
	}
	return hex.EncodeToString(h.Sum(nil))
}
