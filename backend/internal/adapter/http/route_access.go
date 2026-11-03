package http

import (
	"context"
	"crypto/subtle"
	"net/http"
)

// AccessLevel is the weakest caller a route accepts.
type AccessLevel int

const (
	// AccessPublic needs no credentials.
	AccessPublic AccessLevel = iota
	// AccessUser needs any valid session (any account, including
	// self-registered traders when ALLOW_REGISTRATION permits sign-up).
	AccessUser
	// AccessAdmin needs a session whose user has the admin role.
	AccessAdmin
)

func (l AccessLevel) String() string {
	switch l {
	case AccessPublic:
		return "public"
	case AccessUser:
		return "user"
	case AccessAdmin:
		return "admin"
	default:
		return "unknown"
	}
}

// routeRule is the access contract of one ServeMux pattern.
//
// Read covers GET/HEAD; Write covers every other method (POST, PUT, PATCH,
// DELETE, ...). Service marks routes the strategy service calls with the
// shared AUTH_TOKEN (Authorization: Bearer <AUTH_TOKEN>); that token is
// accepted on these routes only, for both reads and writes, and is never a
// session anywhere else.
type routeRule struct {
	Read    AccessLevel
	Write   AccessLevel
	Service bool
}

func (r routeRule) levelFor(method string) AccessLevel {
	if method == http.MethodGet || method == http.MethodHead {
		return r.Read
	}
	return r.Write
}

var (
	userRule      = routeRule{Read: AccessUser, Write: AccessUser}
	userReadAdmin = routeRule{Read: AccessUser, Write: AccessAdmin}
	adminOnlyRule = routeRule{Read: AccessAdmin, Write: AccessAdmin}
	publicRule    = routeRule{Read: AccessPublic, Write: AccessPublic}
	serviceRule   = routeRule{Read: AccessUser, Write: AccessAdmin, Service: true}
)

// unclassifiedRule applies to any pattern missing from routeAccess: reads
// need a session, writes need an admin. routes_access_test.go fails CI until
// the new pattern is classified here.
var unclassifiedRule = userReadAdmin

// routeAccess is the access matrix for every pattern registered on the
// router's ServeMux (handler.go Register* plus each handler's RegisterRoutes
// and the /strategy-api proxy). The service is single-tenant: exchange keys,
// bots, paper engine, notifications, journal and settings are shared by the
// whole server, so writes to them are admin-only. Routes that scope by the
// session's user (finance, DEX wallets, DCA, copy trading, rebalancing,
// SL/TP) stay at user level.
var routeAccess = map[string]routeRule{
	// Bootstrap and health.
	"/api/health":        publicRule,
	"/api/auth/login":    publicRule,
	"/api/auth/register": publicRule, // gated by ALLOW_REGISTRATION (registration.go)
	"/api/auth/config":   publicRule, // {registrationOpen, inviteRequired} only
	"/api/auth/logout":   userRule,
	"/api/auth/me":       userRule,

	// Exchange orders and bots on the server's exchange account.
	"/api/orders/open":       userReadAdmin,
	"/api/orders":            userReadAdmin,
	"/api/orders/":           userReadAdmin,
	"/api/bot/start":         userReadAdmin,
	"/api/bot/stop":          serviceRule, // strategy kill switch stops the Go loop
	"/api/bot/status":        userReadAdmin,
	"/api/portfolio":         userReadAdmin,
	"/api/trades":            userReadAdmin,
	"/api/performance":       userReadAdmin,
	"/api/trading/configure": userReadAdmin,
	"/api/trading/start":     userReadAdmin,
	"/api/trading/stop":      serviceRule, // strategy kill switch stops the Go loop
	"/api/trading/status":    userReadAdmin,
	"/api/trading/portfolio": userReadAdmin,

	// Exchange selection and API keys.
	"/api/exchange":              userReadAdmin,
	"/api/exchange/set":          userReadAdmin,
	"/api/exchange/configure":    userReadAdmin,
	"/api/exchange/balances":     userReadAdmin,
	"/api/exchange/all-balances": userReadAdmin,

	// Server-wide settings (stored as user "default").
	"/api/settings/preferences": userReadAdmin,
	"/api/settings/export":      userReadAdmin,
	"/api/settings/import":      userReadAdmin,
	"/api/settings/reset":       userReadAdmin,

	// Market data.
	"/api/news":             userReadAdmin,
	"/api/sentiment/":       userReadAdmin,
	"/api/signals":          userReadAdmin,
	"/api/market/sentiment": userReadAdmin,

	// Global notification feed and manual journal.
	"/api/notifications":          userReadAdmin,
	"/api/notifications/read-all": userReadAdmin,
	"/api/notifications/":         userReadAdmin,
	"/api/journal":                userReadAdmin,
	"/api/journal/":               userReadAdmin,

	// Per-user SL/TP configs (keyed by the session's user).
	"/api/sltp":  userRule,
	"/api/sltp/": userRule,

	// Personal finance: every handler scopes by the session's user.
	"/api/finance/dashboard":                     userRule,
	"/api/finance/net-worth/history":             userRule,
	"/api/finance/net-worth/calculate":           userRule,
	"/api/finance/net-worth":                     userRule,
	"/api/finance/accounts":                      userRule,
	"/api/finance/accounts/":                     userRule,
	"/api/finance/transactions":                  userRule,
	"/api/finance/transactions/":                 userRule,
	"/api/finance/categories":                    userRule,
	"/api/finance/budgets":                       userRule,
	"/api/finance/budgets/":                      userRule,
	"/api/finance/goals":                         userRule,
	"/api/finance/goals/":                        userRule,
	"/api/finance/assets":                        userRule,
	"/api/finance/assets/":                       userRule,
	"/api/finance/liabilities":                   userRule,
	"/api/finance/liabilities/":                  userRule,
	"/api/finance/subscriptions/upcoming":        userRule,
	"/api/finance/subscriptions":                 userRule,
	"/api/finance/subscriptions/":                userRule,
	"/api/finance/diary/date":                    userRule,
	"/api/finance/diary":                         userRule,
	"/api/finance/diary/":                        userRule,
	"/api/finance/calculators/compound-interest": userRule,
	"/api/finance/calculators/loan":              userRule,
	"/api/finance/calculators/savings-goal":      userRule,
	"/api/finance/calculators/roi":               userRule,
	"/api/finance/calculators/asset-allocation":  userRule,

	// Shared paper engine; the strategy grid bot drives it.
	"/api/paper/order":        serviceRule,
	"/api/paper/portfolio":    userReadAdmin,
	"/api/paper/history":      userReadAdmin,
	"/api/paper/reset":        serviceRule,
	"/api/paper/update-price": serviceRule,
	"/api/paper/orders":       serviceRule,
	"/api/paper/cancel":       serviceRule,
	"/api/paper/seed":         serviceRule,
	"/api/paper/snapshots":    userReadAdmin,

	// Risk, alerts, backtests, metrics, audit.
	"/api/risk/config":        userReadAdmin,
	"/api/risk/metrics":       userReadAdmin,
	"/api/risk/check":         userRule, // stateless evaluation
	"/api/alerts/config":      userReadAdmin,
	"/api/alerts/test":        userReadAdmin, // sends a real alert
	"/api/alerts/history":     userReadAdmin,
	"/api/price-alerts":       userReadAdmin,
	"/api/price-alerts/reset": userReadAdmin,
	"/api/backtest/run":       userReadAdmin, // CPU-heavy, like strategy backtests
	"/api/backtest/history":   userReadAdmin,
	"/api/metrics":            userReadAdmin,
	"/api/audit/logs":         adminOnlyRule, // every user's actions
	"/api/audit/stats":        adminOnlyRule,

	// Real orders on the server's exchange account; the strategy real grid
	// and DCA bots call these with the service token.
	"/api/trade/order":        serviceRule,
	"/api/trade/balances":     serviceRule,
	"/api/trade/ticker":       userReadAdmin,
	"/api/trade/status":       serviceRule,
	"/api/trade/history":      serviceRule,
	"/api/trade/open-orders":  serviceRule,
	"/api/trade/order-status": serviceRule,
	"/api/trade/cancel-order": serviceRule,
	"/api/journal/entry":      serviceRule,
	"/api/journal/exit":       serviceRule,
	"/api/journal/list":       serviceRule,
	"/api/journal/stats":      serviceRule,

	// DEX: wallets and positions are per user; the provider is global.
	"/api/dex/wallets":          userRule,
	"/api/dex/wallets/create":   userRule,
	"/api/dex/wallets/import":   userRule,
	"/api/dex/wallets/load":     userRule,
	"/api/dex/wallets/export":   userRule,
	"/api/dex/balance":          userRule,
	"/api/dex/quote":            userRule,
	"/api/dex/swap":             userRule,
	"/api/dex/approve":          userRule,
	"/api/dex/token":            userRule,
	"/api/dex/allowance":        userRule,
	"/api/dex/liquidity":        userRule,
	"/api/dex/liquidity/add":    userRule,
	"/api/dex/liquidity/remove": userRule,
	"/api/dex/impermanent-loss": userRule,
	"/api/dex/config":           userRule,
	"/api/dex/provider/switch":  userReadAdmin,
	"/api/dex/tx/":              userRule,

	// Per-user bots and portfolios.
	"/api/dashboard/money":    userReadAdmin,
	"/api/rebalance/targets":  userRule,
	"/api/rebalance/analyze":  userRule,
	"/api/rebalance/execute":  userRule,
	"/api/rebalance/history":  userRule,
	"/api/copy/strategies":    userRule,
	"/api/copy/strategies/my": userRule,
	"/api/copy/leaderboard":   userRule,
	"/api/copy/copy":          userRule,
	"/api/copy/copied":        userRule,
	"/api/dca/bots":           userRule,

	// Strategy proxy: reads for any session, writes admin-only (the proxy
	// enforces the same split itself; see strategy_proxy.go).
	StrategyProxyPrefix + "/": userReadAdmin,
}

// ruleFor returns the access rule of the pattern req would be routed to.
func (r *Router) ruleFor(req *http.Request) routeRule {
	if isPublicRoute(req.URL.Path) {
		return publicRule
	}
	_, pattern := r.mux.Handler(req)
	if rule, ok := routeAccess[pattern]; ok && rule != publicRule {
		return rule
	}
	return unclassifiedRule
}

// SetServiceToken sets the shared AUTH_TOKEN the strategy service sends on
// Service routes. Empty disables service access.
func (r *Router) SetServiceToken(token string) {
	r.serviceToken = token
}

func (r *Router) isServiceToken(token string) bool {
	return r.serviceToken != "" && token != "" &&
		subtle.ConstantTimeCompare([]byte(token), []byte(r.serviceToken)) == 1
}

type ctxKeyUserID struct{}

// withUserID stores the authenticated session user on the request context.
func withUserID(req *http.Request, userID string) *http.Request {
	return req.WithContext(context.WithValue(req.Context(), ctxKeyUserID{}, userID))
}

// sessionUserID returns the user the router authenticated, or "".
func sessionUserID(req *http.Request) string {
	v, _ := req.Context().Value(ctxKeyUserID{}).(string)
	return v
}
