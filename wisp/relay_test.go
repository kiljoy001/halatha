//go:build !js && !wasm

package wisp

import (
	"bytes"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// startEcho starts a TCP server that mirrors every byte it reads. Returns its
// address and a cleanup func.
func startEcho(t *testing.T) (addr string, cleanup func()) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_, _ = io.Copy(c, c)
			}(c)
		}
	}()
	return ln.Addr().String(), func() { _ = ln.Close() }
}

// dialRelay starts the Relay under an httptest server and returns a connected
// WebSocket client speaking the WISP protocol.
func dialRelay(t *testing.T, relay *Relay) (*websocket.Conn, *httptest.Server) {
	srv := httptest.NewServer(relay)
	t.Cleanup(srv.Close)
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	dialer := websocket.Dialer{HandshakeTimeout: 5 * time.Second}
	ws, resp, err := dialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial relay: %v (resp=%v)", err, resp)
	}
	return ws, srv
}

// recvFrame reads one WISP frame from the client ws with a timeout.
func recvFrame(t *testing.T, ws *websocket.Conn) Frame {
	_ = ws.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, msg, err := ws.ReadMessage()
	if err != nil {
		t.Fatalf("recvFrame: %v", err)
	}
	f, err := ReadFrame(msg)
	if err != nil {
		t.Fatalf("recvFrame parse: %v", err)
	}
	return f
}

func (f Frame) data() []byte { return f.Payload }

// TestRelayOpenContinue asserts the relay sends CONTINUE(0, large) immediately
// on WebSocket open — the load-bearing invariant that unblocks the v86 client.
func TestRelayOpenContinue(t *testing.T) {
	relay := &Relay{}
	ws, _ := dialRelay(t, relay)
	defer ws.Close()
	f := recvFrame(t, ws)
	if f.Type != TypeContinue || f.StreamID != 0 {
		t.Fatalf("got %d/%d, want CONTINUE/0", f.Type, f.StreamID)
	}
	credit, err := ParseContinue(f.Payload)
	if err != nil {
		t.Fatal(err)
	}
	if credit == 0 {
		t.Fatal("open CONTINUE credit is 0; client would stall")
	}
}

// TestRelayConnectDataEcho exercises the full path: CONNECT a stream to a local
// echo server, send DATA, get the echo back as a DATA frame, then CLOSE.
func TestRelayConnectDataEcho(t *testing.T) {
	echoAddr, echoCleanup := startEcho(t)
	defer echoCleanup()

	relay := &Relay{}
	ws, _ := dialRelay(t, relay)
	defer ws.Close()

	// Drain the open CONTINUE(0).
	_ = recvFrame(t, ws)

	host, port, err := net.SplitHostPort(echoAddr)
	if err != nil {
		t.Fatal(err)
	}
	var p uint16
	for _, ch := range port {
		p = p*10 + uint16(ch-'0')
	}

	// CONNECT stream 5 to the echo server.
	if err := ws.WriteMessage(websocket.BinaryMessage,
		EncodeConnect(5, ProtoTCP, p, host)); err != nil {
		t.Fatal(err)
	}

	// Expect a per-stream CONTINUE(5) ack. (The refresher may also send one; the
	// first frame for stream 5 must be a CONTINUE.)
	f := recvFrame(t, ws)
	if f.Type != TypeContinue || f.StreamID != 5 {
		t.Fatalf("got %d/%d, want CONTINUE/5", f.Type, f.StreamID)
	}

	// Send DATA; the echo server mirrors it back as a DATA frame.
	payload := []byte("hello-wisp")
	if err := ws.WriteMessage(websocket.BinaryMessage,
		EncodeData(5, payload)); err != nil {
		t.Fatal(err)
	}

	// The refresher (1s tick) could interleave a CONTINUE before the echo DATA;
	// accept CONTINUE frames until we see the echoed DATA.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		_ = ws.SetReadDeadline(time.Now().Add(5 * time.Second))
		f = recvFrame(t, ws)
		if f.Type == TypeData && f.StreamID == 5 {
			if !bytes.Equal(f.data(), payload) {
				t.Fatalf("echo DATA = %q, want %q", f.data(), payload)
			}
			goto gotEcho
		}
	}
	t.Fatal("timed out waiting for echoed DATA frame")

gotEcho:
	// Client-initiated CLOSE; the relay should close the conn. The read pump
	// may emit a CLOSE too, but since the stream is already removed it won't.
	if err := ws.WriteMessage(websocket.BinaryMessage,
		EncodeClose(5, ClosePeer)); err != nil {
		t.Fatal(err)
	}
}

// TestRelayDialFailure asserts a CONNECT to a closed port yields a CLOSE frame
// (no goroutine leak, no hang).
func TestRelayDialFailure(t *testing.T) {
	// A port that should refuse: listen+immediately-close to grab a free but
	// dead port.
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	_ = ln.Close()

	relay := &Relay{}
	ws, _ := dialRelay(t, relay)
	defer ws.Close()
	_ = recvFrame(t, ws) // open CONTINUE(0)

	host, portStr, _ := net.SplitHostPort(addr)
	var p uint16
	for _, ch := range portStr {
		p = p*10 + uint16(ch-'0')
	}
	_ = ws.WriteMessage(websocket.BinaryMessage, EncodeConnect(9, ProtoTCP, p, host))

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		f := recvFrame(t, ws)
		if f.Type == TypeClose && f.StreamID == 9 {
			return
		}
	}
	t.Fatal("timed out waiting for CLOSE on dial failure")
}

// TestRelayBadProto asserts a CONNECT with proto≠TCP is rejected with CLOSE.
func TestRelayBadProto(t *testing.T) {
	relay := &Relay{}
	ws, _ := dialRelay(t, relay)
	defer ws.Close()
	_ = recvFrame(t, ws)
	_ = ws.WriteMessage(websocket.BinaryMessage, EncodeConnect(3, 2, 80, "1.2.3.4"))
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		f := recvFrame(t, ws)
		if f.Type == TypeClose && f.StreamID == 3 {
			return
		}
	}
	t.Fatal("timed out waiting for CLOSE on bad proto")
}

// TestRelayHonorsCheckOrigin proves a caller-supplied CheckOrigin is actually
// installed on the upgrader. Inverting relay.defaults()'s `if checkOrigin ==
// nil` silently swaps a rejecting policy for allow-all, which is a
// cross-site WebSocket hijacking hole; this test kills that mutant.
func TestRelayHonorsCheckOrigin(t *testing.T) {
	relay := &Relay{CheckOrigin: func(*http.Request) bool { return false }}
	srv := httptest.NewServer(relay)
	defer srv.Close()

	d := websocket.Dialer{HandshakeTimeout: 5 * time.Second}
	hdr := http.Header{}
	hdr.Set("Origin", "https://evil.example")
	ws, resp, err := d.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), hdr)
	if err == nil {
		ws.Close()
		t.Fatal("upgrade accepted despite CheckOrigin returning false")
	}
	if resp != nil && resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
}

// TestRelayDefaultOriginIsPermissive pins the nil-CheckOrigin default so the
// test above cannot be satisfied by rejecting every upgrade.
func TestRelayDefaultOriginIsPermissive(t *testing.T) {
	relay := &Relay{}
	srv := httptest.NewServer(relay)
	defer srv.Close()

	d := websocket.Dialer{HandshakeTimeout: 5 * time.Second}
	hdr := http.Header{}
	hdr.Set("Origin", "https://anywhere.example")
	ws, _, err := d.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), hdr)
	if err != nil {
		t.Fatalf("default upgrade rejected: %v", err)
	}
	defer ws.Close()
}
