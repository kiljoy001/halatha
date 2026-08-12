//go:build !js && !wasm

package wisp

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// nonceSize is the length of the challenge nonce. tokenSize is the length of the
// single-use admission token the relay mints after a cert verifies.
const (
	nonceSize = 32
	tokenSize = 32
)

// CertAuth is the relay's certificate-based admission gate. A relay with a nil
// or rootless CertAuth is open (local-dev zero-config). With one or more trusted
// CA roots configured, every WebSocket upgrade must present a single-use token
// obtained by proving possession of a certificate that chains to one of those
// roots. Without a valid token the upgrade is refused with 401 before any TCP is
// dialed — the relay's egress is inert.
//
// The intended credential is a short-lived leaf cert issued per node start by a
// CA whose root is anchored on Emercoin NVS (stepca-9p / tpm9p / emercoin-9p).
// The browser page only ferries the nonce, the cert (public), and the signature
// the guest produced; it never holds the cert's private key.
type CertAuth struct {
	// Roots is the PEM-encoded trusted CA root certificate(s). Parsed into a
	// pool on first use. Empty = open relay (no auth).
	Roots [][]byte

	// ChallengeTTL is how long an issued nonce stays valid. Zero = 60s.
	ChallengeTTL time.Duration

	// TokenTTL is how long a minted admission token stays valid. Zero = 60s.
	TokenTTL time.Duration

	mu       sync.Mutex
	pool     *x509.CertPool
	hasRoots bool
	nonces   map[[nonceSize]byte]time.Time
	tokens   map[[tokenSize]byte]time.Time
}

// init parses Roots into a pool. Caller must hold a.mu (or be in a
// single-threaded constructor path).
func (a *CertAuth) init() {
	if a.pool != nil {
		return
	}
	a.pool = x509.NewCertPool()
	a.nonces = make(map[[nonceSize]byte]time.Time)
	a.tokens = make(map[[tokenSize]byte]time.Time)
	for _, b := range a.Roots {
		if a.pool.AppendCertsFromPEM(b) {
			a.hasRoots = true
		}
	}
}

func (a *CertAuth) challengeTTL() time.Duration {
	if a.ChallengeTTL > 0 {
		return a.ChallengeTTL
	}
	return 60 * time.Second
}

func (a *CertAuth) tokenTTL() time.Duration {
	if a.TokenTTL > 0 {
		return a.TokenTTL
	}
	return 60 * time.Second
}

// Enabled reports whether cert auth is enforced (at least one trusted root). A
// nil CertAuth is not enabled — callers may treat "not enabled" as "open relay".
func (a *CertAuth) Enabled() bool {
	if a == nil {
		return false
	}
	a.mu.Lock()
	a.init()
	on := a.hasRoots
	a.mu.Unlock()
	return on
}

// IssueChallenge returns a fresh single-use nonce, recorded with an expiry.
func (a *CertAuth) IssueChallenge() ([]byte, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.init()
	if !a.hasRoots {
		return nil, errors.New("wisp: cert auth not enabled")
	}
	var n [nonceSize]byte
	if _, err := rand.Read(n[:]); err != nil {
		return nil, err
	}
	a.nonces[n] = time.Now().Add(a.challengeTTL())
	return n[:], nil
}

// Verify checks a certificate chain and a signature over sha256(nonce) made with
// the leaf cert's key, consuming the nonce (single-use) and minting a single-use
// admission token. The nonce is consumed before the signature is checked, so a
// bad guess cannot be retried against the same nonce. certBundle is a PEM bundle
// whose first cert is the leaf and whose remaining certs are intermediates the
// relay uses to build the chain to a configured root. Returns the token or an
// error.
func (a *CertAuth) Verify(certBundle, nonce, sig []byte) ([]byte, error) {
	if len(nonce) != nonceSize {
		return nil, errors.New("wisp: bad nonce length")
	}
	var n [nonceSize]byte
	copy(n[:], nonce)

	a.mu.Lock()
	a.init()
	expiry, ok := a.nonces[n]
	if !ok || time.Now().After(expiry) {
		delete(a.nonces, n)
		a.mu.Unlock()
		return nil, errors.New("wisp: nonce expired or unknown")
	}
	delete(a.nonces, n) // single-use: consumed even if the rest fails
	a.mu.Unlock()

	certs, err := parseCerts(certBundle)
	if err != nil {
		return nil, fmt.Errorf("wisp: cert: %w", err)
	}
	leaf := certs[0]
	inters := x509.NewCertPool()
	for _, c := range certs[1:] {
		inters.AddCert(c)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{
		Roots:         a.pool,
		Intermediates: inters,
		KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
	}); err != nil {
		return nil, fmt.Errorf("wisp: cert chain: %w", err)
	}

	digest := sha256.Sum256(nonce)
	if err := verifySig(leaf.PublicKey, digest[:], sig); err != nil {
		return nil, fmt.Errorf("wisp: signature: %w", err)
	}

	var tok [tokenSize]byte
	if _, err := rand.Read(tok[:]); err != nil {
		return nil, err
	}
	a.mu.Lock()
	a.tokens[tok] = time.Now().Add(a.tokenTTL())
	a.mu.Unlock()
	return tok[:], nil
}

// CheckToken validates and consumes a single-use admission token.
func (a *CertAuth) CheckToken(token []byte) bool {
	if len(token) != tokenSize {
		return false
	}
	var t [tokenSize]byte
	copy(t[:], token)
	a.mu.Lock()
	defer a.mu.Unlock()
	expiry, ok := a.tokens[t]
	if !ok || time.Now().After(expiry) {
		delete(a.tokens, t)
		return false
	}
	delete(a.tokens, t) // single-use
	return true
}

// verifySig checks a signature over digest with the cert's public key. The
// signed message is always sha256(nonce) so every key family uses one convention:
// ed25519 signs the digest directly; ECDSA is verified via VerifyASN1 against the
// digest.
func verifySig(pub any, digest, sig []byte) error {
	switch k := pub.(type) {
	case ed25519.PublicKey:
		if ed25519.Verify(k, digest, sig) {
			return nil
		}
	case *ecdsa.PublicKey:
		if ecdsa.VerifyASN1(k, digest, sig) {
			return nil
		}
	default:
		return errors.New("wisp: unsupported cert key type")
	}
	return errors.New("wisp: bad signature")
}

// parseCerts extracts all CERTIFICATE PEM blocks from a bundle. The first is the
// leaf; the rest are intermediates the relay uses to build the chain to a root.
func parseCerts(pemBytes []byte) ([]*x509.Certificate, error) {
	var certs []*x509.Certificate
	rest := pemBytes
	for {
		block, r := pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type == "CERTIFICATE" {
			c, err := x509.ParseCertificate(block.Bytes)
			if err != nil {
				return nil, err
			}
			certs = append(certs, c)
		}
		rest = r
	}
	if len(certs) == 0 {
		return nil, errors.New("no certificates in PEM")
	}
	return certs, nil
}

// CertAuthHandler issues nonces (GET) and exchanges a verified cert + signature
// for a single-use admission token (POST). Mount at /.well-known/wisp/auth. A
// GET returns {"nonce":"<hex>"}; a POST with {"cert":"<PEM>","nonce":"<hex>",
// "sig":"<hex>"} returns {"token":"<hex>"} on success, 401 on any verification
// failure, and 404 when auth is not enabled (so a page probing an open relay
// learns it needs no challenge). CORS is permissive (the page may be cross-origin
// to the relay).
func CertAuthHandler(a *CertAuth) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Cache-Control", "no-store")
		if r.Method == http.MethodOptions {
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if a == nil || !a.Enabled() {
			http.Error(w, "wisp: auth not enabled", http.StatusNotFound)
			return
		}
		switch r.Method {
		case http.MethodGet:
			nonce, err := a.IssueChallenge()
			if err != nil {
				http.Error(w, "wisp: unauthorized", http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"nonce": hex.EncodeToString(nonce)})
		case http.MethodPost:
			var req struct {
				Cert  string `json:"cert"`
				Nonce string `json:"nonce"`
				Sig   string `json:"sig"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, "wisp: bad request", http.StatusBadRequest)
				return
			}
			nonce, err := hex.DecodeString(req.Nonce)
			if err != nil {
				http.Error(w, "wisp: bad nonce", http.StatusBadRequest)
				return
			}
			sig, err := hex.DecodeString(req.Sig)
			if err != nil {
				http.Error(w, "wisp: bad sig", http.StatusBadRequest)
				return
			}
			token, err := a.Verify([]byte(req.Cert), nonce, sig)
			if err != nil {
				http.Error(w, "wisp: unauthorized", http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"token": hex.EncodeToString(token)})
		default:
			http.Error(w, "wisp: method not allowed", http.StatusMethodNotAllowed)
		}
	})
}

// parseHex decodes a hex string, returning nil (not an error) for empty/bad hex.
func parseHex(s string) []byte {
	b, err := hex.DecodeString(s)
	if err != nil {
		return nil
	}
	return b
}
