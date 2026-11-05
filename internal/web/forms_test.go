package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

// formPostReq is a browser form post with a session, as htmx sends it
// when htmx is true.
func formPostReq(host, path, raw string, form url.Values, htmx bool) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "http://"+host+path, strings.NewReader(form.Encode()))
	req.Host = host
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if htmx {
		req.Header.Set("HX-Request", "true")
	}
	req.AddCookie(&http.Cookie{Name: "lotus_session", Value: raw})
	return req
}

func auditCount(t *testing.T, db *sqlx.DB, orgID uuid.UUID, action string) int {
	t.Helper()
	var n int
	withOrgTx(t, db, orgID, func(tx *sqlx.Tx) {
		if err := tx.Get(&n, `SELECT count(*) FROM audit_events WHERE action = $1`, action); err != nil {
			t.Fatalf("count audit %s: %v", action, err)
		}
	})
	return n
}
