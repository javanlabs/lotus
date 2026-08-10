package authn

// Cookie names use the __Host- prefix in production — the only
// browser-enforced cookie protection, per task 1.2 (it forbids the Domain
// attribute, so a cookie can't be scoped wider than the exact host that
// set it). __Host- cookies are rejected outright by browsers unless Secure
// and actually served over HTTPS, which local dev over plain HTTP isn't —
// so in dev mode (cookieSecure=false) these fall back to the same names
// without the prefix, so `curl`/Playwright against plain http://
// localhost still work. The security property __Host- buys only matters
// once real subdomains exist, i.e. in production.
const (
	stateCookieBase         = "lotus_oauth"
	handshakeCookieBase     = "lotus_hs"
	authSessionCookieBase   = "lotus_auth"
	tenantSessionCookieBase = "lotus_session"
)

func cookieName(base string, secure bool) string {
	if secure {
		return "__Host-" + base
	}
	return base
}
