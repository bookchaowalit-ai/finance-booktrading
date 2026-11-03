package websocket

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

const goodToken = "session-token"

func validator(token string) (string, bool) { return "u1", token == goodToken }

func newWSServer(t *testing.T, v func(string) (string, bool)) (*httptest.Server, *WebSocketBroadcasterImpl) {
	t.Helper()
	t.Setenv("FRONTEND_URL", "http://app.test")
	b := NewWebSocketBroadcaster(10, 10)
	go b.Run()
	srv := httptest.NewServer(http.HandlerFunc(NewWSHandler(b, v).HandleWebSocket))
	t.Cleanup(srv.Close)
	return srv, b
}

func dial(t *testing.T, srv *httptest.Server, query string, header http.Header) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	if header == nil {
		header = http.Header{}
	}
	header.Set("Origin", "http://app.test")
	return websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/ws"+query, header)
}

func waitClients(b *WebSocketBroadcasterImpl, want int) bool {
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if b.GetClientCount() == want {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

func TestWSRejectsTokenInQueryString(t *testing.T) {
	srv, b := newWSServer(t, validator)
	_, res, err := dial(t, srv, "?token="+goodToken, nil)
	if err == nil {
		t.Fatal("a token in the URL must not open a socket")
	}
	if res == nil || res.StatusCode != http.StatusBadRequest {
		t.Fatalf("want 400, got %v", res)
	}
	if b.GetClientCount() != 0 {
		t.Fatal("client registered from a query-string token")
	}
}

func TestWSAcceptsAuthorizationHeader(t *testing.T) {
	srv, b := newWSServer(t, validator)
	conn, _, err := dial(t, srv, "", http.Header{"Authorization": {"Bearer " + goodToken}})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	if !waitClients(b, 1) {
		t.Fatal("header-authenticated client was not registered")
	}
}

func TestWSFirstMessageAuth(t *testing.T) {
	srv, b := newWSServer(t, validator)
	conn, _, err := dial(t, srv, "", nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	if b.GetClientCount() != 0 {
		t.Fatal("registered before authenticating")
	}
	if err := conn.WriteJSON(map[string]string{"type": "auth", "token": goodToken}); err != nil {
		t.Fatal(err)
	}
	var ack map[string]string
	conn.SetReadDeadline(time.Now().Add(time.Second))
	if err := conn.ReadJSON(&ack); err != nil || ack["type"] != "auth_ok" {
		t.Fatalf("want auth_ok, got %v err=%v", ack, err)
	}
	if !waitClients(b, 1) {
		t.Fatal("authenticated client was not registered")
	}
}

func TestWSFirstMessageBadTokenCloses(t *testing.T) {
	srv, b := newWSServer(t, validator)
	conn, _, err := dial(t, srv, "", nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	conn.WriteJSON(map[string]string{"type": "auth", "token": "wrong"})
	conn.SetReadDeadline(time.Now().Add(time.Second))
	if _, _, err := conn.ReadMessage(); !websocket.IsCloseError(err, websocket.ClosePolicyViolation) {
		t.Fatalf("want policy-violation close, got %v", err)
	}
	if b.GetClientCount() != 0 {
		t.Fatal("client with a bad token was registered")
	}
}

func TestWSClosesWhenNoAuthArrives(t *testing.T) {
	old := wsAuthTimeout
	wsAuthTimeout = 50 * time.Millisecond
	t.Cleanup(func() { wsAuthTimeout = old })

	srv, _ := newWSServer(t, validator)
	conn, _, err := dial(t, srv, "", nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	conn.SetReadDeadline(time.Now().Add(time.Second))
	if _, _, err := conn.ReadMessage(); err == nil {
		t.Fatal("socket stayed open without authenticating")
	}
}

func TestWSWithoutValidatorFailsClosed(t *testing.T) {
	srv, _ := newWSServer(t, nil)
	if _, _, err := dial(t, srv, "", http.Header{"Authorization": {"Bearer anything"}}); err == nil {
		t.Fatal("no validator must mean no socket")
	}
}

func TestWSOversizedFirstMessageCloses(t *testing.T) {
	// Accept any token, so only the size limit can reject the message.
	srv, b := newWSServer(t, func(string) (string, bool) { return "u1", true })
	conn, _, err := dial(t, srv, "", nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	conn.WriteJSON(map[string]string{"type": "auth", "token": strings.Repeat("x", 64<<10)})
	conn.SetReadDeadline(time.Now().Add(time.Second))
	if _, _, err := conn.ReadMessage(); err == nil {
		t.Fatal("an oversized pre-auth message must close the socket")
	}
	if b.GetClientCount() != 0 {
		t.Fatal("client registered after an oversized message")
	}
}
