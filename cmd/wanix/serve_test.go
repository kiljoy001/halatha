//go:build !js && !wasm

package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"tractor.dev/wanix/wisp"
)

// mkRootPEM builds a self-signed ECDSA P-256 CA root and returns its PEM.
func mkRootPEM(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// TestWanixConfigHandler locks the discovery contract the browser page relies
// on: GET /.well-known/wanix must return JSON {"wisp":"/.well-known/wisp"} with a
// permissive CORS header. With cert auth enabled it also advertises
// auth+auth-endpoint; the page's resolveRelayURL reads cfg.wisp, so a change
// here breaks zero-config discovery.
func TestWanixConfigHandler(t *testing.T) {
	srv := httptest.NewServer(wanixConfigHandler(nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}
	if ac := resp.Header.Get("Access-Control-Allow-Origin"); ac != "*" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want *", ac)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Wisp         string `json:"wisp"`
		Auth         string `json:"auth"`
		AuthEndpoint string `json:"auth-endpoint"`
	}
	if err := json.Unmarshal(body, &cfg); err != nil {
		t.Fatalf("unmarshal %q: %v", body, err)
	}
	if cfg.Wisp != "/.well-known/wisp" {
		t.Fatalf("wisp = %q, want /.well-known/wisp", cfg.Wisp)
	}
	if cfg.Auth != "" || cfg.AuthEndpoint != "" {
		t.Fatalf("open relay advertised auth: %+v", cfg)
	}
}

// TestWanixConfigHandlerAuthed asserts the discovery document advertises the
// cert auth flow when a CA root is configured, so the page knows to fetch a
// nonce, get the guest to sign, and exchange cert+sig for a token before opening
// the WS.
func TestWanixConfigHandlerAuthed(t *testing.T) {
	rootPEM := mkRootPEM(t)
	auth := &wisp.CertAuth{Roots: [][]byte{rootPEM}}
	srv := httptest.NewServer(wanixConfigHandler(auth))
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	var cfg struct {
		Wisp         string `json:"wisp"`
		Auth         string `json:"auth"`
		AuthEndpoint string `json:"auth-endpoint"`
	}
	if err := json.Unmarshal(body, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Auth != "cert" {
		t.Fatalf("auth = %q, want cert", cfg.Auth)
	}
	if cfg.AuthEndpoint != "/.well-known/wisp/auth" {
		t.Fatalf("auth-endpoint = %q, want /.well-known/wisp/auth", cfg.AuthEndpoint)
	}
}

// TestBuildCertAuth checks the flag parsing: a valid PEM file is kept, an
// unreadable/garbage path is dropped, and no valid roots yields nil (open).
func TestBuildCertAuth(t *testing.T) {
	rootPEM := mkRootPEM(t)
	dir := t.TempDir()
	good := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(good, rootPEM, 0644); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(dir, "bad.pem")
	if err := os.WriteFile(bad, []byte("not pem"), 0644); err != nil {
		t.Fatal(err)
	}

	a := buildCertAuth([]string{good, bad, "/nonexistent/path"})
	if a == nil || !a.Enabled() {
		t.Fatal("expected enabled auth with 1 valid root file")
	}
	if len(a.Roots) != 2 {
		t.Fatalf("roots = %d, want 2 (good + bad kept for the pool to parse)", len(a.Roots))
	}

	if buildCertAuth(nil) != nil {
		t.Fatal("nil paths should yield nil (open) auth")
	}
	if buildCertAuth([]string{bad}) != nil {
		t.Fatal("all-garbage paths should yield nil (open) auth")
	}
}

// TestCheckOriginAllowed pins the cross-site WebSocket hijacking policy used
// by the public deployment. Any loosening of this predicate lets a hostile
// page open a WISP relay session against a visitor's browser.
func TestCheckOriginAllowed(t *testing.T) {
	allowed := []string{
		"", // non-browser clients send no Origin
		"https://halatha.rentonsoftworks.com",
		"http://localhost:7654",
		"http://127.0.0.1:7654",
		"http://localhost:3000",
	}
	denied := []string{
		"https://evil.example",
		"http://halatha.rentonsoftworks.com",         // plaintext, not the TLS origin
		"https://halatha.rentonsoftworks.com.evil.io", // suffix attack
		"https://evil.io/?x=https://halatha.rentonsoftworks.com",
		"http://127.0.0.1:9999",
		"null",
	}

	for _, o := range allowed {
		r := httptest.NewRequest("GET", "/.well-known/wisp", nil)
		if o != "" {
			r.Header.Set("Origin", o)
		}
		if !checkOriginAllowed(r) {
			t.Errorf("origin %q rejected, want allowed", o)
		}
	}
	for _, o := range denied {
		r := httptest.NewRequest("GET", "/.well-known/wisp", nil)
		r.Header.Set("Origin", o)
		if checkOriginAllowed(r) {
			t.Errorf("origin %q allowed, want rejected", o)
		}
	}
}
