//go:build !js && !wasm

package main

import (
	"bytes"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/knusbaum/go9p/proto"
	"tractor.dev/wanix/fs"
	"tractor.dev/wanix/fs/memfs"
	"tractor.dev/wanix/fs/p9p"
)

// Nothing else pins the dialect of the services wanix serve offers, which is
// how a 9P2000.L server once ended up on the port a 9front guest mounts.
// 9P2000 is the only dialect wanix speaks: a Plan 9 kernel asks for "9P2000"
// and must be given it, and a 9P2000.L request must be refused.

func served(t *testing.T) fs.FS {
	t.Helper()
	backing := memfs.New()
	if err := fs.WriteFile(backing, "hello", []byte("from wanix serve"), 0644); err != nil {
		t.Fatal(err)
	}
	return backing
}

// The guest's TCP listener, reached through the WISP relay.
func TestGuest9PListenerSpeaks9P2000(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go serve9P(served(t), ln)

	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	imported, err := p9p.ClientFS(c, "none", "")
	if err != nil {
		t.Fatalf("9P2000 mount refused: %v", err)
	}
	got, err := fs.ReadFile(imported, "hello")
	if err != nil || string(got) != "from wanix serve" {
		t.Fatalf("read over the guest listener: %q %v", got, err)
	}

	c2, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()
	if v := versionOver(t, c2, "9P2000.L"); v == "9P2000.L" {
		t.Error("guest listener accepted 9P2000.L")
	}
}

// The page's WebSocket import of the served namespace.
func TestWebSocket9PSpeaks9P2000(t *testing.T) {
	fsys := served(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p9Handler(fsys, w, r)
	}))
	defer srv.Close()
	url := "ws" + strings.TrimPrefix(srv.URL, "http")

	for _, tc := range []struct {
		ask  string
		want bool
	}{{"9P2000", true}, {"9P2000.L", false}} {
		ws, _, err := websocket.DefaultDialer.Dial(url, nil)
		if err != nil {
			t.Fatal(err)
		}
		req := (&proto.TRVersion{Header: proto.Header{Type: proto.Tversion, Tag: 0xffff}, Msize: 8192, Version: tc.ask}).Compose()
		if err := ws.WriteMessage(websocket.BinaryMessage, req); err != nil {
			t.Fatal(err)
		}
		_, msg, err := ws.ReadMessage()
		ws.Close()
		if err != nil {
			t.Fatal(err)
		}
		r, err := proto.ParseCall(bytes.NewReader(msg))
		if err != nil {
			t.Fatal(err)
		}
		v, ok := r.(*proto.TRVersion)
		accepted := ok && v.Version == tc.ask
		if accepted != tc.want {
			t.Errorf("websocket asked for %s: got %T %+v, want accepted=%v", tc.ask, r, r, tc.want)
		}
	}
}

// versionOver sends one Tversion and returns the version the server chose,
// or "" if it answered with an error.
func versionOver(t *testing.T, c net.Conn, ask string) string {
	t.Helper()
	req := (&proto.TRVersion{Header: proto.Header{Type: proto.Tversion, Tag: 0xffff}, Msize: 8192, Version: ask}).Compose()
	if _, err := c.Write(req); err != nil {
		t.Fatal(err)
	}
	r, err := proto.ParseCall(c)
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := r.(*proto.TRVersion); ok {
		return v.Version
	}
	return ""
}
