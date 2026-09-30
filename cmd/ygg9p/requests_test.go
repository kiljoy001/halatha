package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"

	"9fans.net/go/plan9"
	"9fans.net/go/plan9/client"
)

const requestClient = "bdbacfd82240de3dcd123924cbb55256fb8dab08aa98e305528ab84f419e6efb"
const requestTransport = "430ba825e9812a0cb1fe6f3880373034d5fe1637067301d562b74288fa4e31d1"

func TestRequestsNeverEnrollAndAreBounded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	original := []byte(`{"serverKey":"` + serverKey + `","exports":[]}`)
	os.WriteFile(path, original, 0600)
	proposal := requestSubmission{"register", requestClient, requestTransport}
	r, err := submitRequest(path, proposal)
	if err != nil {
		t.Fatal(err)
	}
	if r.IdentityVerified || !validRequestID(r.ID) {
		t.Fatal("submission falsely authenticated")
	}
	if _, err := submitRequest(path, proposal); err == nil {
		t.Fatal("duplicate pending key admitted")
	}
	cfg, err := load(path)
	if err != nil || len(cfg.Keys) != 0 {
		t.Fatal("request enrolled a key", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(path), "images")); !os.IsNotExist(err) {
		t.Fatal("request allocated storage")
	}
	for i := 1; i < maxPendingRequests; i++ {
		seed := make([]byte, 32)
		seed[0] = byte(i)
		key := ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)
		proposal.ClientKey = hex.EncodeToString(key)
		if _, err := submitRequest(path, proposal); err != nil {
			t.Fatal(err)
		}
	}
	proposal.ClientKey = serverKey
	if _, err := submitRequest(path, proposal); err == nil {
		t.Fatal("request inbox limit bypassed")
	}
	if err := resolveRequest(path, "../config.json"); err == nil {
		t.Fatal("request path traversal")
	}
	if err := resolveRequest(path, r.ID); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if !bytes.Equal(b, original) {
		t.Fatal("request/rejection modified allowlist")
	}
}
func TestRequest9POnlyAcceptsProposals(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	os.WriteFile(path, []byte(`{"serverKey":"`+serverKey+`","exports":[]}`), 0600)
	a, b := net.Pipe()
	done := make(chan error, 1)
	const observed = "200:89f8:c3a4:a07f:32c2:f446:894c:696a" // test server key's address
	go func() { defer b.Close(); done <- serveRequests(b, b, path, observed) }()
	defer func() { a.Close(); <-done }()
	c, err := client.NewConn(a)
	if err != nil {
		t.Fatal(err)
	}
	fs, err := c.Attach(nil, "none", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, outside := range []string{"config.json", "images", "../config.json", "requests"} {
		if _, err := fs.Stat(outside); err == nil {
			t.Fatal("administrative tree exposed", outside)
		}
	}
	if _, err := fs.Create("new", plan9.OWRITE, 0666); err == nil {
		t.Fatal("arbitrary creation accepted")
	}
	request, err := fs.Open("request", plan9.OWRITE|plan9.OTRUNC)
	if err != nil {
		t.Fatal(err)
	}
	defer request.Close()
	forged := []byte(`{"action":"register","clientKey":"` + requestClient + `","transportKey":"` + requestTransport + `","identityVerified":true}`)
	if _, err := request.Write(forged); err == nil {
		t.Fatal("forged verification field admitted")
	}
	valid, _ := json.Marshal(requestSubmission{"register", requestClient, requestTransport})
	if _, err := request.WriteAt(valid, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := request.WriteAt(valid, 0); err == nil {
		t.Fatal("multiple proposals accepted on one connection")
	}
	status, err := fs.Open("status", plan9.OREAD)
	if err != nil {
		t.Fatal(err)
	}
	defer status.Close()
	var record keyRequest
	if err := json.NewDecoder(status).Decode(&record); err != nil {
		t.Fatal(err)
	}
	if record.ClientKey != requestClient || record.IdentityVerified || record.ObservedPeer != observed {
		t.Fatal(record)
	}
	cfg, err := load(path)
	if err != nil || len(cfg.Keys) != 0 {
		t.Fatal("network submission granted access", err)
	}
}

func TestRequestPeerMustUseYggdrasil(t *testing.T) {
	for _, address := range []string{"", "192.168.2.211", "127.0.0.1", "::1", "2001:db8::1", "300::1"} {
		if requestPeerAllowed(net.ParseIP(address), "") == nil {
			t.Fatalf("accepted non-node %q", address)
		}
	}
	peer := net.ParseIP("200:89f8:c3a4:a07f:32c2:f446:894c:696a")
	if err := requestPeerAllowed(peer, ""); err != nil {
		t.Fatal(err)
	}
	if requestPeerAllowed(peer, requestTransport) == nil {
		t.Fatal("ignored explicit transport restriction")
	}
}
