//go:build !js && !wasm

package wisp

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// mkCA builds a CA certificate. parent==nil and parentKey==nil means self-signed
// (the key's own cert is the issuer); otherwise the cert is signed by parent.
func mkCA(t *testing.T, parent *x509.Certificate, parentKey crypto.Signer, pub any, serial int64, cn string) (*x509.Certificate, []byte) {
	t.Helper()
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(serial),
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	issuer := tmpl
	var signer crypto.Signer = parentKey
	if parent == nil {
		// self-signed: sign with the key itself
		signer = parentKey
		issuer = tmpl
	} else {
		issuer = parent
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, issuer, pub, signer)
	if err != nil {
		t.Fatalf("mkCA: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("mkCA parse: %v", err)
	}
	return cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// mkRootCA returns a self-signed ECDSA P-256 root CA.
func mkRootCA(t *testing.T) (*x509.Certificate, *ecdsa.PrivateKey, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert, pemBytes := mkCA(t, nil, key, &key.PublicKey, 1, "test-ca")
	return cert, key, pemBytes
}

// mkLeaf builds a leaf (non-CA) certificate signed by parent.
func mkLeaf(t *testing.T, parent *x509.Certificate, parentKey crypto.Signer, pub any, serial int64) (*x509.Certificate, []byte) {
	t.Helper()
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(serial),
		Subject:      pkix.Name{CommonName: "node"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, parent, pub, parentKey)
	if err != nil {
		t.Fatalf("mkLeaf: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("mkLeaf parse: %v", err)
	}
	return cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// signDigest signs sha256(nonce) with priv. ed25519 signs the digest directly;
// ECDSA signs it via ASN.1 — matching verifySig's convention.
func signDigest(t *testing.T, priv any, digest []byte) []byte {
	t.Helper()
	switch s := priv.(type) {
	case ed25519.PrivateKey:
		return ed25519.Sign(s, digest)
	case *ecdsa.PrivateKey:
		sig, err := ecdsa.SignASN1(rand.Reader, s, digest)
		if err != nil {
			t.Fatal(err)
		}
		return sig
	default:
		t.Fatalf("unsupported signer %T", priv)
		return nil
	}
}

func sha256sum(b []byte) []byte {
	d := sha256.Sum256(b)
	return d[:]
}

func TestCertAuthDisabledIsOpen(t *testing.T) {
	var a *CertAuth
	if a.Enabled() {
		t.Fatal("nil CertAuth should not be enabled")
	}
	if (&CertAuth{}).Enabled() {
		t.Fatal("rootless CertAuth should not be enabled")
	}
	if (&CertAuth{Roots: [][]byte{[]byte("not pem")}}).Enabled() {
		t.Fatal("bad-root CertAuth should not be enabled")
	}
}

func TestCertAuthIssueAndVerify(t *testing.T) {
	rootCert, rootKey, rootPEM := mkRootCA(t)
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	_, leafPEM := mkLeaf(t, rootCert, rootKey, &leafKey.PublicKey, 2)
	a := &CertAuth{Roots: [][]byte{rootPEM}}

	nonce, err := a.IssueChallenge()
	if err != nil {
		t.Fatal(err)
	}
	sig := signDigest(t, leafKey, sha256sum(nonce))
	tok, err := a.Verify(leafPEM, nonce, sig)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !a.CheckToken(tok) {
		t.Fatal("minted token rejected")
	}
	// Token is single-use.
	if a.CheckToken(tok) {
		t.Fatal("reused token accepted — token must be single-use")
	}
}

func TestCertAuthEd25519Leaf(t *testing.T) {
	rootCert, rootKey, rootPEM := mkRootCA(t) // ECDSA root signs an ed25519 leaf.
	edPub, edPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	_, leafPEM := mkLeaf(t, rootCert, rootKey, edPub, 2)
	a := &CertAuth{Roots: [][]byte{rootPEM}}

	nonce, _ := a.IssueChallenge()
	sig := signDigest(t, edPriv, sha256sum(nonce))
	tok, err := a.Verify(leafPEM, nonce, sig)
	if err != nil {
		t.Fatalf("ed25519 leaf verify: %v", err)
	}
	if !a.CheckToken(tok) {
		t.Fatal("ed25519 token rejected")
	}
}

func TestCertAuthIntermediateChain(t *testing.T) {
	rootCert, rootKey, rootPEM := mkRootCA(t)
	interKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	interCert, interPEM := mkCA(t, rootCert, rootKey, &interKey.PublicKey, 2, "test-inter")
	leafKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	_, leafPEM := mkLeaf(t, interCert, interKey, &leafKey.PublicKey, 3)

	// Relay trusts only the root; the client must present leaf + intermediate.
	bundle := append([]byte{}, leafPEM...)
	bundle = append(bundle, interPEM...)
	a := &CertAuth{Roots: [][]byte{rootPEM}}

	nonce, _ := a.IssueChallenge()
	sig := signDigest(t, leafKey, sha256sum(nonce))
	tok, err := a.Verify(bundle, nonce, sig)
	if err != nil {
		t.Fatalf("chain verify: %v", err)
	}
	if !a.CheckToken(tok) {
		t.Fatal("chain token rejected")
	}

	// Without the intermediate in the bundle, the chain can't be built.
	if _, err := a.Verify(leafPEM, nonce, sig); err == nil {
		t.Fatal("leaf without intermediate accepted — chain should not validate")
	}
}

func TestCertAuthUntrustedRoot(t *testing.T) {
	_, _, rootPEM := mkRootCA(t)
	otherRoot, otherKey, _ := mkRootCA(t)
	leafKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	_, leafPEM := mkLeaf(t, otherRoot, otherKey, &leafKey.PublicKey, 2)
	a := &CertAuth{Roots: [][]byte{rootPEM}}

	nonce, _ := a.IssueChallenge()
	sig := signDigest(t, leafKey, sha256sum(nonce))
	if _, err := a.Verify(leafPEM, nonce, sig); err == nil {
		t.Fatal("cert from an untrusted CA accepted")
	}
}

func TestCertAuthBadSig(t *testing.T) {
	rootCert, rootKey, rootPEM := mkRootCA(t)
	leafKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	otherKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	_, leafPEM := mkLeaf(t, rootCert, rootKey, &leafKey.PublicKey, 2)
	a := &CertAuth{Roots: [][]byte{rootPEM}}

	nonce, _ := a.IssueChallenge()
	badSig := signDigest(t, otherKey, sha256sum(nonce)) // signed by a different key
	if _, err := a.Verify(leafPEM, nonce, badSig); err == nil {
		t.Fatal("signature by a non-matching key accepted")
	}
}

func TestCertAuthNonceSingleUse(t *testing.T) {
	rootCert, rootKey, rootPEM := mkRootCA(t)
	leafKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	_, leafPEM := mkLeaf(t, rootCert, rootKey, &leafKey.PublicKey, 2)
	a := &CertAuth{Roots: [][]byte{rootPEM}}

	nonce, _ := a.IssueChallenge()
	sig := signDigest(t, leafKey, sha256sum(nonce))
	if _, err := a.Verify(leafPEM, nonce, sig); err != nil {
		t.Fatal(err)
	}
	// Replay: the nonce was consumed; even a fresh correct sig must fail.
	sig2 := signDigest(t, leafKey, sha256sum(nonce))
	if _, err := a.Verify(leafPEM, nonce, sig2); err == nil {
		t.Fatal("replayed nonce accepted — nonce must be single-use")
	}
}

func TestCertAuthBadGuessConsumesNonce(t *testing.T) {
	rootCert, rootKey, rootPEM := mkRootCA(t)
	leafKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	_, leafPEM := mkLeaf(t, rootCert, rootKey, &leafKey.PublicKey, 2)
	a := &CertAuth{Roots: [][]byte{rootPEM}}

	nonce, _ := a.IssueChallenge()
	badSig := make([]byte, 8) // clearly invalid
	if _, err := a.Verify(leafPEM, nonce, badSig); err == nil {
		t.Fatal("zero signature accepted")
	}
	// The bad attempt consumed the nonce; the correct sig now fails too.
	sig := signDigest(t, leafKey, sha256sum(nonce))
	if _, err := a.Verify(leafPEM, nonce, sig); err == nil {
		t.Fatal("nonce not consumed by bad guess — online guessing would be possible")
	}
}

func TestCertAuthNonceExpiry(t *testing.T) {
	rootCert, rootKey, rootPEM := mkRootCA(t)
	leafKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	_, leafPEM := mkLeaf(t, rootCert, rootKey, &leafKey.PublicKey, 2)
	a := &CertAuth{Roots: [][]byte{rootPEM}, ChallengeTTL: 20 * time.Millisecond}

	nonce, _ := a.IssueChallenge()
	sig := signDigest(t, leafKey, sha256sum(nonce))
	time.Sleep(40 * time.Millisecond)
	if _, err := a.Verify(leafPEM, nonce, sig); err == nil {
		t.Fatal("expired nonce accepted")
	}
}

// TestCertAuthTokenExpiry proves an admission token stops working once its
// TTL lapses. Mutating CheckToken's `!ok || time.Now().After(expiry)` to
// `!ok && ...` makes expired tokens admissible; this test kills that mutant.
func TestCertAuthTokenExpiry(t *testing.T) {
	rootCert, rootKey, rootPEM := mkRootCA(t)
	leafKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	_, leafPEM := mkLeaf(t, rootCert, rootKey, &leafKey.PublicKey, 2)
	a := &CertAuth{Roots: [][]byte{rootPEM}, TokenTTL: 20 * time.Millisecond}

	nonce, err := a.IssueChallenge()
	if err != nil {
		t.Fatal(err)
	}
	sig := signDigest(t, leafKey, sha256sum(nonce))
	tok, err := a.Verify(leafPEM, nonce, sig)
	if err != nil {
		t.Fatal(err)
	}

	time.Sleep(40 * time.Millisecond)
	if a.CheckToken(tok) {
		t.Fatal("expired token accepted")
	}
}

// TestCertAuthTokenValidBeforeExpiry pins the other side of the boundary so
// the expiry check cannot be satisfied by rejecting everything.
func TestCertAuthTokenValidBeforeExpiry(t *testing.T) {
	rootCert, rootKey, rootPEM := mkRootCA(t)
	leafKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	_, leafPEM := mkLeaf(t, rootCert, rootKey, &leafKey.PublicKey, 2)
	a := &CertAuth{Roots: [][]byte{rootPEM}, TokenTTL: 10 * time.Second}

	nonce, err := a.IssueChallenge()
	if err != nil {
		t.Fatal(err)
	}
	sig := signDigest(t, leafKey, sha256sum(nonce))
	tok, err := a.Verify(leafPEM, nonce, sig)
	if err != nil {
		t.Fatal(err)
	}
	if !a.CheckToken(tok) {
		t.Fatal("fresh token rejected")
	}
}

func TestCertAuthHandler(t *testing.T) {
	rootCert, rootKey, rootPEM := mkRootCA(t)
	leafKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	_, leafPEM := mkLeaf(t, rootCert, rootKey, &leafKey.PublicKey, 2)
	a := &CertAuth{Roots: [][]byte{rootPEM}}
	srv := httptest.NewServer(CertAuthHandler(a))
	defer srv.Close()

	// GET nonce.
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Nonce string `json:"nonce"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	nonce, err := hex.DecodeString(got.Nonce)
	if err != nil || len(nonce) != nonceSize {
		t.Fatalf("bad nonce %q", got.Nonce)
	}

	// POST cert+sig → token.
	body, _ := json.Marshal(map[string]string{
		"cert":  string(leafPEM),
		"nonce": hex.EncodeToString(nonce),
		"sig":   hex.EncodeToString(signDigest(t, leafKey, sha256sum(nonce))),
	})
	resp2, err := http.Post(srv.URL, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if resp2.StatusCode != 200 {
		t.Fatalf("POST status = %d, want 200", resp2.StatusCode)
	}
	var tok struct {
		Token string `json:"token"`
	}
	json.NewDecoder(resp2.Body).Decode(&tok)
	resp2.Body.Close()
	token, err := hex.DecodeString(tok.Token)
	if err != nil || len(token) != tokenSize {
		t.Fatalf("bad token %q", tok.Token)
	}
	if !a.CheckToken(token) {
		t.Fatal("token from handler did not validate")
	}

	// Disallowed (bad sig) → 401.
	nonce2, _ := a.IssueChallenge()
	badBody, _ := json.Marshal(map[string]string{
		"cert":  string(leafPEM),
		"nonce": hex.EncodeToString(nonce2),
		"sig":   hex.EncodeToString(make([]byte, 8)),
	})
	resp3, _ := http.Post(srv.URL, "application/json", bytes.NewReader(badBody))
	if resp3.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bad-sig status = %d, want 401", resp3.StatusCode)
	}
	resp3.Body.Close()

	// Disabled auth → 404 (page learns no challenge is needed).
	disabled := httptest.NewServer(CertAuthHandler(&CertAuth{}))
	defer disabled.Close()
	resp4, _ := http.Get(disabled.URL)
	if resp4.StatusCode != http.StatusNotFound {
		t.Fatalf("disabled status = %d, want 404", resp4.StatusCode)
	}
	resp4.Body.Close()
}

// TestRelayCertAuthGate wires CertAuth into a Relay: an authed WS upgrade (with a
// valid ?token=) connects and gets the open CONTINUE(0), while a bare upgrade is
// rejected before the WebSocket opens (TCP stays inert).
func TestRelayCertAuthGate(t *testing.T) {
	rootCert, rootKey, rootPEM := mkRootCA(t)
	leafKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	_, leafPEM := mkLeaf(t, rootCert, rootKey, &leafKey.PublicKey, 2)
	relay := &Relay{Auth: &CertAuth{Roots: [][]byte{rootPEM}}}
	srv := httptest.NewServer(relay)
	defer srv.Close()
	wsBase := "ws" + strings.TrimPrefix(srv.URL, "http")

	nonce, err := relay.Auth.IssueChallenge()
	if err != nil {
		t.Fatal(err)
	}
	sig := signDigest(t, leafKey, sha256sum(nonce))
	tok, err := relay.Auth.Verify(leafPEM, nonce, sig)
	if err != nil {
		t.Fatal(err)
	}

	// Authed upgrade: connects and immediately receives CONTINUE(0).
	d := websocket.Dialer{HandshakeTimeout: 5 * time.Second}
	authed, _, err := d.Dial(wsBase+"?token="+hex.EncodeToString(tok), nil)
	if err != nil {
		t.Fatalf("authed upgrade failed: %v", err)
	}
	_ = recvFrame(t, authed) // open CONTINUE(0)
	authed.Close()

	// Bare upgrade (no token): rejected, dial fails.
	if bare, _, err := d.Dial(wsBase, nil); err == nil {
		bare.Close()
		t.Fatal("bare upgrade accepted; cert gate did not enforce")
	}

	// Bogus token: rejected.
	if bad, _, err := d.Dial(wsBase+"?token=deadbeef", nil); err == nil {
		bad.Close()
		t.Fatal("bogus-token upgrade accepted")
	}
}
