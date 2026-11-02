package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"tractor.dev/wanix/fs"
	"tractor.dev/wanix/fs/p9p"
)

func TestSessionLayout(t *testing.T) {
	s, err := newSession()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"ram", "host"} {
		if _, err := fs.Stat(s, name); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

func testServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	srv := httptest.NewServer((&server{tokens: newTokenStore(time.Minute)}).mux())
	t.Cleanup(srv.Close)
	resp, err := http.Get(srv.URL + "/token")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var tok struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tok); err != nil {
		t.Fatal(err)
	}
	if len(tok.Token) != 64 {
		t.Fatalf("token %q", tok.Token)
	}
	return srv, tok.Token
}

func dialSession(t *testing.T, srv *httptest.Server, token string) (*wsStream, error) {
	t.Helper()
	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/session"
	if token != "" {
		url += "?token=" + token
	}
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		return nil, err
	}
	return &wsStream{conn: conn}, nil
}

func TestSession9POverWebSocket(t *testing.T) {
	srv, token := testServer(t)
	stream, err := dialSession(t, srv, token)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()

	fsys, err := p9p.ClientFS(stream, "none", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := fs.MkdirAll(fsys, "ram/home/glenda", 0o777); err != nil {
		t.Fatal(err)
	}
	if err := fs.WriteFile(fsys, "ram/home/glenda/hi", []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	data, err := fs.ReadFile(fsys, "ram/home/glenda/hi")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello\n" {
		t.Fatalf("ram read %q", data)
	}
	if err := fs.MkdirAll(fsys, "host/uploads", 0o777); err != nil {
		t.Fatal(err)
	}
	if err := fs.WriteFile(fsys, "host/uploads/ctl", []byte("open"), 0o666); err != nil {
		t.Fatal(err)
	}
	ctl, err := fs.ReadFile(fsys, "host/uploads/ctl")
	if err != nil {
		t.Fatal(err)
	}
	if string(ctl) != "open" {
		t.Fatalf("ctl read %q", ctl)
	}
}

func TestTokenIsSingleUse(t *testing.T) {
	srv, token := testServer(t)
	stream, err := dialSession(t, srv, token)
	if err != nil {
		t.Fatal(err)
	}
	stream.Close()

	if _, err := dialSession(t, srv, token); err == nil {
		t.Fatal("replayed token was accepted")
	}
	if _, err := dialSession(t, srv, ""); err == nil {
		t.Fatal("missing token was accepted")
	}
}

func TestTokenExpires(t *testing.T) {
	store := newTokenStore(time.Millisecond)
	tok, err := store.issue()
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond)
	if store.consume(tok) {
		t.Fatal("expired token was accepted")
	}
}
