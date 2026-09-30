//go:build !js && !wasm

package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"9fans.net/go/plan9"
	"github.com/gorilla/websocket"
	"tractor.dev/wanix/fs"
	"tractor.dev/wanix/fs/memfs"
	"tractor.dev/wanix/fs/p9p"
)

type websocketStream struct {
	conn   *websocket.Conn
	reader io.Reader
}

func (w *websocketStream) Read(p []byte) (int, error) {
	r := import9PReader{ws: w.conn, reader: w.reader}
	n, err := r.Read(p)
	w.reader = r.reader
	return n, err
}
func (w *websocketStream) Write(p []byte) (int, error) {
	if err := w.conn.WriteMessage(websocket.BinaryMessage, p); err != nil {
		return 0, err
	}
	return len(p), nil
}
func (w *websocketStream) Close() error { return w.conn.Close() }

type testMountSigner struct {
	key   ed25519.PrivateKey
	calls atomic.Int32
}

func (s *testMountSigner) Sign(_ context.Context, message []byte) (ed25519.PublicKey, []byte, error) {
	s.calls.Add(1)
	return s.key.Public().(ed25519.PublicKey), ed25519.Sign(s.key, message), nil
}

type mountFixture struct {
	server  *httptest.Server
	gateway *import9PGateway
	signer  *testMountSigner
	key     ed25519.PrivateKey
	backing fs.FS
	dials   atomic.Int32
}

func newMountFixture(t *testing.T, ttl time.Duration) *mountFixture {
	t.Helper()
	f := &mountFixture{backing: memfs.New()}
	if err := fs.WriteFile(f.backing, "hello", []byte("network fixture"), 0644); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			f.dials.Add(1)
			go func() { defer c.Close(); p9p.Serve(f.backing, c, c) }()
		}
	}()
	_, f.key, err = ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	pub := f.key.Public().(ed25519.PublicKey)
	f.signer = &testMountSigner{key: f.key}
	f.server = httptest.NewUnstartedServer(nil)
	origin := "http://" + f.server.Listener.Addr().String()
	f.gateway, err = newImport9PGateway(import9PConfig{Target: ln.Addr().String(), Origin: origin, Export: "network", Keys: map[string]ed25519.PublicKey{hex.EncodeToString(pub): pub}, Signer: f.signer, ChallengeTTL: ttl})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/.well-known/import9p", f.gateway)
	mux.Handle("/.well-known/import9p/sign", f.gateway.SignHandler())
	f.server.Config.Handler = mux
	f.server.Start()
	t.Cleanup(f.server.Close)
	return f
}
func (f *mountFixture) connect(t *testing.T) (*websocket.Conn, mountChallenge, []byte) {
	t.Helper()
	ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(f.server.URL, "http")+"/.well-known/import9p", http.Header{"Origin": {f.server.URL}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ws.Close() })
	ws.SetReadDeadline(time.Now().Add(3 * time.Second))
	var envelope struct{ Type, Payload string }
	if err := ws.ReadJSON(&envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Type != "mount-challenge" {
		t.Fatal(envelope)
	}
	payload, err := base64.RawURLEncoding.DecodeString(envelope.Payload)
	if err != nil {
		t.Fatal(err)
	}
	var challenge mountChallenge
	if err := json.Unmarshal(payload, &challenge); err != nil {
		t.Fatal(err)
	}
	if challenge.Protocol != mountProtocol || challenge.Export != "network" || challenge.Access != "read-only" || challenge.Server != f.server.URL || challenge.Origin != f.server.URL {
		t.Fatal(challenge)
	}
	return ws, challenge, payload
}
func proofFor(key ed25519.PrivateKey, c mountChallenge, message []byte) mountProof {
	return mountProof{"mount-proof", c.Session, hex.EncodeToString(key.Public().(ed25519.PublicKey)), hex.EncodeToString(ed25519.Sign(key, message))}
}
func readyMount(t *testing.T, ws *websocket.Conn, proof mountProof) {
	t.Helper()
	if err := ws.WriteJSON(proof); err != nil {
		t.Fatal(err)
	}
	var r struct{ Type string }
	if err := ws.ReadJSON(&r); err != nil {
		t.Fatal(err)
	}
	if r.Type != "mount-ready" {
		t.Fatal(r)
	}
}
func deniedMount(t *testing.T, ws *websocket.Conn) {
	t.Helper()
	_, _, err := ws.ReadMessage()
	if !websocket.IsCloseError(err, websocket.ClosePolicyViolation) {
		t.Fatalf("expected policy denial, got %v", err)
	}
}

func TestImport9PGatewayMountsFixedTarget(t *testing.T) {
	f := newMountFixture(t, 0)
	ws, c, payload := f.connect(t)
	readyMount(t, ws, proofFor(f.key, c, payload))
	mounted, err := p9p.ClientFS(&websocketStream{conn: ws}, "none", "")
	if err != nil {
		t.Fatal(err)
	}
	data, err := fs.ReadFile(mounted, "hello")
	if err != nil || string(data) != "network fixture" {
		t.Fatalf("read %q %v", data, err)
	}
	// Raw clients cannot bypass the read-only restriction by emitting Twrite.
	packet, err := (&plan9.Fcall{Type: plan9.Twrite, Tag: 99, Fid: 2, Data: []byte("corrupt")}).Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if err := ws.WriteMessage(websocket.BinaryMessage, packet); err != nil {
		t.Fatal(err)
	}
	deniedMount(t, ws)
	data, err = fs.ReadFile(f.backing, "hello")
	if err != nil || string(data) != "network fixture" {
		t.Fatalf("mutated data %q %v", data, err)
	}
}
func TestImport9PDeniesBadProofBeforeDial(t *testing.T) {
	for _, name := range []string{"unknown-key", "signature", "export", "target", "access", "origin", "server", "aname", "user", "replay", "binary-before-auth", "expired"} {
		t.Run(name, func(t *testing.T) {
			ttl := time.Duration(0)
			if name == "expired" {
				ttl = 40 * time.Millisecond
			}
			f := newMountFixture(t, ttl)
			ws, c, payload := f.connect(t)
			proof := proofFor(f.key, c, payload)
			switch name {
			case "unknown-key":
				_, other, _ := ed25519.GenerateKey(nil)
				proof = proofFor(other, c, payload)
			case "signature":
				proof.Signature = strings.Repeat("00", 64)
			case "replay":
				old := proof
				ws.Close()
				ws, c, _ = f.connect(t)
				proof = old
				proof.Session = c.Session
			case "binary-before-auth":
				ws.WriteMessage(websocket.BinaryMessage, []byte("9P"))
				deniedMount(t, ws)
				if f.dials.Load() != 0 {
					t.Fatal("dialed before authorization")
				}
				return
			case "expired":
				time.Sleep(60 * time.Millisecond)
			default:
				switch name {
				case "export":
					c.Export = "secret"
				case "target":
					c.Target = "127.0.0.1:22"
				case "access":
					c.Access = "read-write"
				case "origin":
					c.Origin = "https://evil.example"
				case "server":
					c.Server = "https://other.example"
				case "aname":
					c.Aname = "secret"
				case "user":
					c.User = "root"
				}
				changed, _ := json.Marshal(c)
				proof = proofFor(f.key, c, changed)
			}
			ws.WriteJSON(proof)
			deniedMount(t, ws)
			if f.dials.Load() != 0 {
				t.Fatal("dialed before authorization")
			}
		})
	}
}
func TestImport9PRejectsOriginHostAndDestination(t *testing.T) {
	f := newMountFixture(t, 0)
	url := "ws" + strings.TrimPrefix(f.server.URL, "http") + "/.well-known/import9p"
	for _, tc := range []struct {
		suffix, origin, host string
		code                 int
	}{
		{"?target=127.0.0.1:22", f.server.URL, "", 400}, {"", "https://unrelated.example", "", 403}, {"", "", "", 403}, {"", f.server.URL, "rebound.example", 403},
	} {
		h := http.Header{}
		if tc.origin != "" {
			h.Set("Origin", tc.origin)
		}
		if tc.host != "" {
			h.Set("Host", tc.host)
		}
		ws, r, err := websocket.DefaultDialer.Dial(url+tc.suffix, h)
		if ws != nil {
			ws.Close()
			t.Fatal("unexpected upgrade")
		}
		if err == nil || r == nil || r.StatusCode != tc.code {
			t.Fatalf("%+v: %v %v", tc, r, err)
		}
	}
	if f.dials.Load() != 0 {
		t.Fatal("unauthorized origin caused dial")
	}
}
func (f *mountFixture) signRequest(t *testing.T, body, origin string) (int, mountProof) {
	t.Helper()
	r, err := http.NewRequest("POST", f.server.URL+"/.well-known/import9p/sign", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Origin", origin)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+f.gateway.pairingToken)
	response, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var proof mountProof
	if response.StatusCode == 200 {
		if err := json.NewDecoder(response.Body).Decode(&proof); err != nil {
			t.Fatal(err)
		}
	}
	return response.StatusCode, proof
}
func TestImport9PRestrictedSigner(t *testing.T) {
	f := newMountFixture(t, 0)
	ws, c, _ := f.connect(t)
	body := fmtSession(c.Session)
	for _, tc := range []struct{ body, origin string }{{body, "https://evil.example"}, {fmtSession("unknown"), f.server.URL}, {`{"session":"` + c.Session + `","message":"arbitrary"}`, f.server.URL}, {body + ` {}`, f.server.URL}} {
		status, _ := f.signRequest(t, tc.body, tc.origin)
		if status == 200 {
			t.Fatal("signed unauthorized input")
		}
	}
	if f.signer.calls.Load() != 0 {
		t.Fatal("unauthorized input reached signer")
	}
	status, proof := f.signRequest(t, body, f.server.URL)
	if status != 200 {
		t.Fatal(status)
	}
	if status, _ := f.signRequest(t, body, f.server.URL); status == 200 {
		t.Fatal("challenge signed twice")
	}
	readyMount(t, ws, proof)
	if f.signer.calls.Load() != 1 {
		t.Fatal("unexpected signing count")
	}
	if status, _ := f.signRequest(t, body, f.server.URL); status == 200 {
		t.Fatal("signed consumed challenge")
	}
}
func fmtSession(id string) string {
	b, _ := json.Marshal(struct {
		Session string `json:"session"`
	}{id})
	return string(b)
}
func TestImport9PReadOnlyPolicy(t *testing.T) {
	f := newMountFixture(t, 0)
	for _, call := range []*plan9.Fcall{
		{Type: plan9.Topen, Mode: plan9.ORDWR}, {Type: plan9.Topen, Mode: plan9.OTRUNC}, {Type: plan9.Topen, Mode: plan9.ORCLOSE}, {Type: plan9.Twrite}, {Type: plan9.Tcreate}, {Type: plan9.Tremove}, {Type: plan9.Twstat}, {Type: plan9.Tauth},
		{Type: plan9.Tattach, Uname: "root", Afid: plan9.NOFID}, {Type: plan9.Tattach, Uname: "none", Aname: "secret", Afid: plan9.NOFID}, {Type: plan9.Tversion, Version: "9P2000.L", Msize: 8192},
	} {
		if f.gateway.permit9P(call) {
			t.Fatalf("permitted %+v", call)
		}
	}
}
func TestImport9PFramingCannotHideWrite(t *testing.T) {
	f := newMountFixture(t, 0)
	ws, c, payload := f.connect(t)
	readyMount(t, ws, proofFor(f.key, c, payload))
	version, _ := (&plan9.Fcall{Type: plan9.Tversion, Tag: plan9.NOTAG, Msize: 8192, Version: "9P2000"}).Bytes()
	write, _ := (&plan9.Fcall{Type: plan9.Twrite, Tag: 1, Fid: 1, Data: []byte("corrupt")}).Bytes()
	ws.WriteMessage(websocket.BinaryMessage, version[:3])
	ws.WriteMessage(websocket.BinaryMessage, version[3:])
	_, _, err := ws.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	ws.WriteMessage(websocket.BinaryMessage, bytes.Join([][]byte{write[:4], write[4:]}, nil))
	deniedMount(t, ws)
}

func TestImport9PSignerRequiresPairing(t *testing.T) {
	f := newMountFixture(t, 0)
	_, c, _ := f.connect(t)
	for _, token := range []string{"", "Bearer wrong"} {
		r, _ := http.NewRequest("POST", f.server.URL+"/.well-known/import9p/sign", strings.NewReader(fmtSession(c.Session)))
		r.Header.Set("Origin", f.server.URL)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", token)
		response, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != 403 {
			t.Fatalf("unpaired request: %d", response.StatusCode)
		}
	}
	if f.signer.calls.Load() != 0 {
		t.Fatal("unpaired client reached factotum")
	}
}
