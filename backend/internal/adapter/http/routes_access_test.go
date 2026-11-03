package http

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// registeredPatterns parses the package sources and cmd/main.go and returns
// every pattern passed to ServeMux.HandleFunc / Handle. It reads the real
// route table, so a route added anywhere shows up here without editing the
// test.
func registeredPatterns(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	files = append(files, filepath.Join("..", "..", "..", "cmd", "main.go"))
	seen := map[string]bool{}
	fset := token.NewFileSet()
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) != 2 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || (sel.Sel.Name != "HandleFunc" && sel.Sel.Name != "Handle") {
				return true
			}
			pattern, ok := patternLiteral(call.Args[0])
			if !ok {
				t.Errorf("%s: route pattern is not a constant expression; classify it by hand", fset.Position(call.Pos()))
				return true
			}
			if pattern == "/ws" { // WebSocket server on its own port, own auth
				return true
			}
			seen[pattern] = true
			return true
		})
	}
	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// patternLiteral resolves "lit" and StrategyProxyPrefix + "lit".
func patternLiteral(e ast.Expr) (string, bool) {
	switch v := e.(type) {
	case *ast.BasicLit:
		s, err := strconv.Unquote(v.Value)
		return s, err == nil
	case *ast.SelectorExpr:
		if v.Sel.Name == "StrategyProxyPrefix" {
			return StrategyProxyPrefix, true
		}
	case *ast.Ident:
		if v.Name == "StrategyProxyPrefix" {
			return StrategyProxyPrefix, true
		}
	case *ast.BinaryExpr:
		l, okL := patternLiteral(v.X)
		r, okR := patternLiteral(v.Y)
		return l + r, okL && okR
	}
	return "", false
}

func TestRouteTableIsFullyClassified(t *testing.T) {
	patterns := registeredPatterns(t)
	if len(patterns) < 100 {
		t.Fatalf("found only %d route patterns; the source scan is broken", len(patterns))
	}
	registered := map[string]bool{}
	for _, p := range patterns {
		registered[p] = true
		if _, ok := routeAccess[p]; !ok {
			t.Errorf("route %q is not in routeAccess (route_access.go): classify it as public/user/admin/service", p)
		}
	}
	for p := range routeAccess {
		if !registered[p] {
			t.Errorf("routeAccess has %q but no handler registers it: remove the stale entry", p)
		}
	}
}

// Only the bootstrap endpoints may be reached without credentials.
func TestOnlyBootstrapRoutesArePublic(t *testing.T) {
	allowed := map[string]bool{"/api/health": true, "/api/auth/login": true, "/api/auth/register": true, "/api/auth/config": true}
	for p, rule := range routeAccess {
		if (rule.Read == AccessPublic || rule.Write == AccessPublic) && !allowed[p] {
			t.Errorf("%s is reachable anonymously; only %v may be public", p, allowed)
		}
		if rule.Service && rule.Write != AccessAdmin {
			t.Errorf("%s accepts the service token but its session writes are %s, want admin", p, rule.Write)
		}
	}
}

// Pins the levels of the routes that move money or change server-wide state,
// so a reclassification has to be a deliberate edit here too.
func TestCriticalRouteLevels(t *testing.T) {
	cases := []struct {
		pattern string
		method  string
		want    AccessLevel
		service bool
	}{
		{"/api/trade/order", http.MethodPost, AccessAdmin, true},
		{"/api/trade/cancel-order", http.MethodPost, AccessAdmin, true},
		{"/api/orders", http.MethodPost, AccessAdmin, false},
		{"/api/orders/", http.MethodDelete, AccessAdmin, false},
		{"/api/exchange/configure", http.MethodPost, AccessAdmin, false},
		{"/api/exchange/set", http.MethodPost, AccessAdmin, false},
		{"/api/trading/configure", http.MethodPost, AccessAdmin, false},
		{"/api/trading/start", http.MethodPost, AccessAdmin, false},
		{"/api/bot/start", http.MethodPost, AccessAdmin, false},
		// The strategy kill switch stops the Go grid loop with the service token.
		{"/api/bot/stop", http.MethodPost, AccessAdmin, true},
		{"/api/trading/stop", http.MethodPost, AccessAdmin, true},
		{"/api/settings/import", http.MethodPost, AccessAdmin, false},
		{"/api/settings/export", http.MethodPost, AccessAdmin, false},
		{"/api/paper/reset", http.MethodPost, AccessAdmin, true},
		{"/api/risk/config", http.MethodPost, AccessAdmin, false},
		{"/api/alerts/test", http.MethodPost, AccessAdmin, false},
		{"/api/dex/provider/switch", http.MethodPost, AccessAdmin, false},
		{"/api/audit/logs", http.MethodGet, AccessAdmin, false},
		{StrategyProxyPrefix + "/", http.MethodPost, AccessAdmin, false},
		{StrategyProxyPrefix + "/", http.MethodGet, AccessUser, false},
		{"/api/finance/accounts", http.MethodPost, AccessUser, false},
		{"/api/dex/swap", http.MethodPost, AccessUser, false},
		{"/api/health", http.MethodGet, AccessPublic, false},
		{"/api/auth/config", http.MethodGet, AccessPublic, false},
	}
	for _, c := range cases {
		rule, ok := routeAccess[c.pattern]
		if !ok {
			t.Errorf("%s missing from routeAccess", c.pattern)
			continue
		}
		if got := rule.levelFor(c.method); got != c.want || rule.Service != c.service {
			t.Errorf("%s %s = %s service=%t, want %s service=%t", c.method, c.pattern, got, rule.Service, c.want, c.service)
		}
	}
}

const (
	matrixUserSession  = "matrix-user-session"
	matrixAdminSession = "matrix-admin-session"
	matrixServiceToken = "matrix-service-token"
)

func newMatrixRouter(t *testing.T, patterns []string) *Router {
	t.Helper()
	sessions := &memorySessionStore{}
	_ = sessions.SetSession(context.Background(), matrixUserSession, "matrix-trader", time.Hour)
	_ = sessions.SetSession(context.Background(), matrixAdminSession, "matrix-admin", time.Hour)
	auth := &AuthHandler{sessions: sessions, users: newMemoryUserStore(
		authUser{ID: "matrix-admin", Email: "admin@example.test", Role: RoleAdmin},
		authUser{ID: "matrix-trader", Email: "trader@example.test", Role: "trader"},
	)}
	router := NewRouter(auth)
	router.SetServiceToken(matrixServiceToken)
	for _, p := range patterns {
		router.mux.HandleFunc(p, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		})
	}
	return router
}

// Drives the real Router gate over every registered pattern, every method
// and every kind of caller.
func TestRouteAccessMatrix(t *testing.T) {
	patterns := registeredPatterns(t)
	router := newMatrixRouter(t, patterns)
	methods := []string{http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete}
	callers := []string{"anonymous", "bad-token", "service", "user", "admin"}
	n := 0
	for _, pattern := range patterns {
		path := pattern
		if strings.HasSuffix(path, "/") {
			path += "x"
		}
		rule, ok := routeAccess[pattern]
		if !ok {
			rule = unclassifiedRule
		}
		for _, method := range methods {
			level := rule.levelFor(method)
			for _, caller := range callers {
				want := http.StatusNoContent
				switch caller {
				case "anonymous", "bad-token":
					if level != AccessPublic {
						want = http.StatusUnauthorized
					}
				case "service":
					if level != AccessPublic && !rule.Service {
						want = http.StatusUnauthorized
					}
				case "user":
					if level == AccessAdmin {
						want = http.StatusForbidden
					}
				}
				req := httptest.NewRequest(method, path, nil)
				switch caller {
				case "bad-token":
					req.Header.Set("Authorization", "Bearer not-a-session")
				case "service":
					req.Header.Set("Authorization", "Bearer "+matrixServiceToken)
				case "user":
					req.Header.Set("Authorization", "Bearer "+matrixUserSession)
				case "admin":
					req.Header.Set("Authorization", "Bearer "+matrixAdminSession)
				}
				n++
				req.RemoteAddr = "10.9." + strconv.Itoa(n/250) + "." + strconv.Itoa(n%250) + ":1234"
				res := httptest.NewRecorder()
				router.ServeHTTP(res, req)
				if res.Code != want {
					t.Errorf("%s %s as %s (level %s, service=%t): got %d want %d", method, path, caller, level, rule.Service, res.Code, want)
				}
			}
		}
	}
}

// The service token is not a session: it must not unlock user routes, and an
// empty configured token must never match an empty or missing header.
func TestServiceTokenScope(t *testing.T) {
	router := newMatrixRouter(t, []string{"/api/trade/order", "/api/finance/accounts"})
	do := func(path, token string) int {
		req := httptest.NewRequest(http.MethodPost, path, nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		req.RemoteAddr = "10.8.0." + strconv.Itoa(len(path)+len(token)) + ":1"
		res := httptest.NewRecorder()
		router.ServeHTTP(res, req)
		return res.Code
	}
	if got := do("/api/trade/order", matrixServiceToken); got != http.StatusNoContent {
		t.Fatalf("service token on service route: %d", got)
	}
	if got := do("/api/finance/accounts", matrixServiceToken); got != http.StatusUnauthorized {
		t.Fatalf("service token on user route: %d, want 401", got)
	}
	router.SetServiceToken("")
	if got := do("/api/trade/order", matrixServiceToken); got != http.StatusUnauthorized {
		t.Fatalf("disabled service token accepted: %d", got)
	}
	if router.isServiceToken("") {
		t.Fatal("empty token matched empty service token")
	}
}

// SL/TP configs are keyed by the session user, never a client header.
func TestSLTPIgnoresSpoofedUserHeader(t *testing.T) {
	h := NewSLTPHandler()
	router := newMatrixRouter(t, nil)
	router.RegisterSLTPRoutes(h)
	req := httptest.NewRequest(http.MethodPost, "/api/sltp", strings.NewReader(`{"symbol":"BTCUSDT","stopLossPercent":2,"takeProfitPercent":4}`))
	req.Header.Set("Authorization", "Bearer "+matrixUserSession)
	req.Header.Set("X-User-ID", "matrix-admin")
	req.RemoteAddr = "10.7.0.1:1"
	res := httptest.NewRecorder()
	router.ServeHTTP(res, req)
	if res.Code >= 300 {
		t.Fatalf("create SL/TP: %d %s", res.Code, res.Body.String())
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	if _, ok := h.configs[sltpKey("matrix-admin", "BTCUSDT")]; ok {
		t.Fatal("X-User-ID header chose the config owner")
	}
	if _, ok := h.configs[sltpKey("matrix-trader", "BTCUSDT")]; !ok {
		t.Fatalf("config not stored under the session user: %v", h.configs)
	}
}
