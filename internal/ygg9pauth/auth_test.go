package ygg9pauth

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"9fans.net/go/plan9"
)

func testKey(n byte) (ed25519.PublicKey, ed25519.PrivateKey) {
	seed := make([]byte, 32)
	seed[0] = n
	key := ed25519.NewKeyFromSeed(seed)
	return key.Public().(ed25519.PublicKey), key
}
func testConfig(t *testing.T, access string) (Config, ed25519.PrivateKey, *atomic.Int32, chan *plan9.Fcall) {
	t.Helper()
	pub, priv := testKey(1)
	server, _ := testKey(2)
	id := hex.EncodeToString(pub)
	opened := new(atomic.Int32)
	calls := make(chan *plan9.Fcall, 10)
	cfg := Config{ServerKey: server, Keys: map[string]ed25519.PublicKey{id: pub}, Exports: map[string]Export{id: {Root: "/private/alice", Access: access, Open: func() (io.ReadWriteCloser, error) {
		opened.Add(1)
		a, b := net.Pipe()
		go func() {
			defer b.Close()
			for {
				f, err := Read(b)
				if err != nil {
					return
				}
				calls <- f
				r := &plan9.Fcall{Type: f.Type + 1, Tag: f.Tag, Qid: plan9.Qid{Type: plan9.QTDIR, Path: 2}, Count: uint32(len(f.Data))}
				if f.Type == plan9.Tversion {
					r.Version = "9P2000"
					r.Msize = f.Msize
				}
				if err := plan9.WriteFcall(b, r); err != nil {
					return
				}
			}
		}()
		return a, nil
	}}}}
	return cfg, priv, opened, calls
}
func start(t *testing.T, cfg Config, peer net.IP) net.Conn {
	t.Helper()
	a, b := net.Pipe()
	a.SetDeadline(time.Now().Add(3 * time.Second))
	done := make(chan error, 1)
	go func() { done <- Serve(b, peer, cfg) }()
	t.Cleanup(func() {
		a.Close()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("server did not stop")
		}
	})
	return a
}
func rpc(t *testing.T, c net.Conn, f *plan9.Fcall) *plan9.Fcall {
	t.Helper()
	if err := plan9.WriteFcall(c, f); err != nil {
		t.Fatal(err)
	}
	r, err := Read(c)
	if err != nil {
		t.Fatal(err)
	}
	if r.Tag != f.Tag {
		t.Fatal("wrong tag")
	}
	return r
}
func challenge(t *testing.T, c net.Conn) []byte {
	t.Helper()
	r := rpc(t, c, &plan9.Fcall{Type: plan9.Tversion, Tag: plan9.NOTAG, Msize: 8192, Version: "9P2000"})
	if r.Type != plan9.Rversion {
		t.Fatal(r)
	}
	r = rpc(t, c, &plan9.Fcall{Type: plan9.Tauth, Tag: 1, Afid: 42, Uname: "none"})
	if r.Type != plan9.Rauth || r.Aqid.Type != plan9.QTAUTH {
		t.Fatal(r)
	}
	var message []byte
	for {
		r = rpc(t, c, &plan9.Fcall{Type: plan9.Tread, Tag: 2, Fid: 42, Offset: uint64(len(message)), Count: 47})
		if r.Type != plan9.Rread {
			t.Fatal(r)
		}
		if len(r.Data) == 0 {
			break
		}
		message = append(message, r.Data...)
	}
	return message
}
func proof(key ed25519.PrivateKey, message []byte) []byte {
	b, _ := json.Marshal(Proof{hex.EncodeToString(key.Public().(ed25519.PublicKey)), hex.EncodeToString(ed25519.Sign(key, message))})
	return b
}

func TestAuthenticatedPrivateWritableExport(t *testing.T) {
	cfg, key, opened, calls := testConfig(t, "read-write")
	c := start(t, cfg, Address(key.Public().(ed25519.PublicKey)))
	message := challenge(t, c)
	if opened.Load() != 0 {
		t.Fatal("opened export before proof")
	}
	var scope Challenge
	if err := json.Unmarshal(message, &scope); err != nil {
		t.Fatal(err)
	}
	if scope.Export != "/private/alice" || scope.Access != "read-write" || scope.Protocol != Protocol {
		t.Fatal(scope)
	}
	r := rpc(t, c, &plan9.Fcall{Type: plan9.Twrite, Tag: 3, Fid: 42, Data: proof(key, message)})
	if r.Type != plan9.Rwrite {
		t.Fatal(r)
	}
	if opened.Load() != 0 {
		t.Fatal("opened export before attach")
	}
	r = rpc(t, c, &plan9.Fcall{Type: plan9.Tattach, Tag: 4, Fid: 0, Afid: 42, Uname: "none"})
	if r.Type != plan9.Rattach {
		t.Fatal(r)
	}
	if opened.Load() != 1 {
		t.Fatal("wrong export count")
	}
	<-calls
	f := <-calls
	if f.Afid != plan9.NOFID {
		t.Fatal("auth fid forwarded to exporter")
	}
	r = rpc(t, c, &plan9.Fcall{Type: plan9.Tclunk, Tag: 5, Fid: 42})
	if r.Type != plan9.Rclunk {
		t.Fatal(r)
	}
	r = rpc(t, c, &plan9.Fcall{Type: plan9.Tcreate, Tag: 6, Fid: 0, Name: "work", Mode: plan9.ORDWR, Perm: 0600})
	if r.Type != plan9.Rcreate {
		t.Fatal(r)
	}
	if (<-calls).Name != "work" {
		t.Fatal("create not forwarded")
	}
	r = rpc(t, c, &plan9.Fcall{Type: plan9.Twrite, Tag: 7, Fid: 0, Data: []byte("saved")})
	if r.Count != 5 {
		t.Fatal(r)
	}
	if string((<-calls).Data) != "saved" {
		t.Fatal("write not forwarded")
	}
}

func TestRejectAnonymousAndForgedProof(t *testing.T) {
	for _, kind := range []string{"anonymous", "signature", "other-key", "scope", "replay", "wrong-export"} {
		t.Run(kind, func(t *testing.T) {
			cfg, key, opened, _ := testConfig(t, "read-write")
			peer := Address(key.Public().(ed25519.PublicKey))
			c := start(t, cfg, peer)
			if kind == "anonymous" {
				r := rpc(t, c, &plan9.Fcall{Type: plan9.Tattach, Tag: 1, Fid: 0, Afid: plan9.NOFID, Uname: "none"})
				if r.Type != plan9.Rerror {
					t.Fatal(r)
				}
			} else {
				message := challenge(t, c)
				signed := message
				signer := key
				switch kind {
				case "signature":
					signed = []byte("wrong nonce")
				case "other-key":
					_, signer = testKey(3)
				case "scope":
					var s Challenge
					json.Unmarshal(message, &s)
					s.Export = "/"
					signed, _ = json.Marshal(s)
				case "replay":
					other := start(t, cfg, peer)
					signed = challenge(t, other)
				}
				r := rpc(t, c, &plan9.Fcall{Type: plan9.Twrite, Tag: 3, Fid: 42, Data: proof(signer, signed)})
				if kind == "wrong-export" {
					if r.Type != plan9.Rwrite {
						t.Fatal(r)
					}
					r = rpc(t, c, &plan9.Fcall{Type: plan9.Tattach, Tag: 4, Fid: 0, Afid: 42, Uname: "none", Aname: "/"})
				}
				if r.Type != plan9.Rerror {
					t.Fatal("proof accepted", r)
				}
			}
			if opened.Load() != 0 {
				t.Fatal("denied client opened export")
			}
		})
	}
}

func TestRejectUnapprovedSource(t *testing.T) {
	for _, source := range []net.IP{net.ParseIP("192.168.2.2"), net.ParseIP("200:123::1")} {
		cfg, _, opened, _ := testConfig(t, "read-write")
		c := start(t, cfg, source)
		if _, err := c.Read(make([]byte, 1)); err == nil {
			t.Fatal("source accepted")
		}
		if opened.Load() != 0 {
			t.Fatal("export opened")
		}
	}
}
func TestAddressMatchesYggdrasil(t *testing.T) {
	// Public test key derived from the seed sha256("halatha test server key");
	// the expected address was computed by yggdrasil-go's address.AddrForKey.
	key, _ := hex.DecodeString("bb039e2dafc0669e85dcbb59cb4acc59f1960db693fd826743d98745e180b898")
	if got := Address(key).String(); got != "200:89f8:c3a4:a07f:32c2:f446:894c:696a" {
		t.Fatal(got)
	}
	key = []byte{189, 186, 207, 216, 34, 64, 222, 61, 205, 18, 57, 36, 203, 181, 82, 86, 251, 141, 171, 8, 170, 152, 227, 5, 82, 138, 184, 79, 65, 158, 110, 251}
	if !Address(key).Equal(net.IP{2, 0, 132, 138, 96, 79, 187, 126, 67, 132, 101, 219, 141, 182, 104, 149}) {
		t.Fatal("upstream vector differs")
	}
}
func TestEmptyAllowlistDenies(t *testing.T) {
	server, _ := testKey(2)
	cfg := Config{ServerKey: server}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	c := start(t, cfg, net.ParseIP("200:123::1"))
	if _, err := c.Read(make([]byte, 1)); err == nil {
		t.Fatal("empty allowlist accepted")
	}
}

type delegatedSigner ed25519.PrivateKey

func (k delegatedSigner) Sign(_ context.Context, b []byte) (ed25519.PublicKey, []byte, error) {
	p := ed25519.PrivateKey(k)
	return p.Public().(ed25519.PublicKey), ed25519.Sign(p, b), nil
}
func TestDelegatedTransportAuthenticatesClient(t *testing.T) {
	cfg, key, opened, _ := testConfig(t, "read-write")
	pub := key.Public().(ed25519.PublicKey)
	id := hex.EncodeToString(pub)
	transport, _ := testKey(3)
	e := cfg.Exports[id]
	e.TransportKeys = []ed25519.PublicKey{transport}
	cfg.Exports[id] = e
	c := start(t, cfg, Address(transport))
	scope := Scope{ServerKey: cfg.ServerKey, ClientKey: pub, TransportKey: transport, Root: e.Root, Access: e.Access}
	if err := Authenticate(context.Background(), c, scope, delegatedSigner(key)); err != nil {
		t.Fatal(err)
	}
	if opened.Load() != 0 {
		t.Fatal("export opened before attach")
	}
	r := rpc(t, c, &plan9.Fcall{Type: plan9.Tattach, Tag: 20, Fid: 0, Afid: plan9.NOFID, Uname: "none"})
	if r.Type != plan9.Rattach || opened.Load() != 1 {
		t.Fatal("delegated attach failed", r)
	}
}
func TestDelegatedTransportCannotImpersonateClient(t *testing.T) {
	for _, wrong := range []string{"transport-signer", "different-client", "unapproved-source", "wrong-pinned-transport", "unenrolled-client"} {
		t.Run(wrong, func(t *testing.T) {
			cfg, key, opened, _ := testConfig(t, "read-write")
			pub := key.Public().(ed25519.PublicKey)
			id := hex.EncodeToString(pub)
			transport, transportPrivate := testKey(3)
			other, otherPrivate := testKey(4)
			e := cfg.Exports[id]
			e.TransportKeys = []ed25519.PublicKey{transport}
			cfg.Exports[id] = e
			peer := Address(transport)
			signer := key
			pinned := transport
			switch wrong {
			case "transport-signer":
				signer = transportPrivate
			case "different-client":
				signer = otherPrivate
			case "unapproved-source":
				peer = Address(other)
			case "wrong-pinned-transport":
				pinned = other
			case "unenrolled-client":
				pub = other
				signer = otherPrivate
			}
			c := start(t, cfg, peer)
			err := Authenticate(context.Background(), c, Scope{ServerKey: cfg.ServerKey, ClientKey: pub, TransportKey: pinned, Root: e.Root, Access: e.Access}, delegatedSigner(signer))
			if err == nil {
				t.Fatal("unauthorized delegation accepted")
			}
			if opened.Load() != 0 {
				t.Fatal("opened private export")
			}
		})
	}
}
