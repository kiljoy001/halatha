//go:build !js && !wasm

package main

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"tractor.dev/wanix/internal/ygg9pauth"
)

const mountProtocol = "halatha-mount-v1"

type mountChallenge struct {
	Protocol string `json:"protocol"`
	Session  string `json:"session"`
	Server   string `json:"server"`
	Target   string `json:"target"`
	Export   string `json:"export"`
	Access   string `json:"access"`
	User     string `json:"user"`
	Aname    string `json:"aname"`
	Origin   string `json:"origin"`
	Nonce    string `json:"nonce"`
	Expires  int64  `json:"expires"`
}

type mountProof struct {
	Type      string `json:"type"`
	Session   string `json:"session"`
	PublicKey string `json:"publicKey"`
	Signature string `json:"signature"`
}

type mountSigner interface {
	Sign(context.Context, []byte) (ed25519.PublicKey, []byte, error)
}

type import9PConfig struct {
	Target, Origin, Export, User, Aname string
	Keys                                map[string]ed25519.PublicKey
	Signer                              mountSigner
	ChallengeTTL                        time.Duration
	RemoteScope                         *ygg9pauth.Scope
}

type pendingMount struct {
	message  []byte
	deadline time.Time
	signing  bool
	signed   bool
	used     bool
}

type import9PGateway struct {
	cfg          import9PConfig
	host         string
	pairingToken string
	mu           sync.Mutex
	pending      map[string]*pendingMount
	slots        chan struct{}
}

func newImport9PGateway(cfg import9PConfig) (*import9PGateway, error) {
	u, err := url.Parse(cfg.Origin)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("mount origin must be an exact http(s) origin without a path")
	}
	if cfg.Target == "" || cfg.Export == "" || len(cfg.Keys) == 0 {
		return nil, errors.New("mount target, export and approved public keys are required")
	}
	if cfg.User == "" {
		cfg.User = "none"
	}
	if cfg.ChallengeTTL == 0 {
		cfg.ChallengeTTL = 30 * time.Second
	}
	if cfg.ChallengeTTL <= 0 || cfg.ChallengeTTL > time.Minute {
		return nil, errors.New("mount challenge lifetime must be between zero and one minute")
	}
	keys := make(map[string]ed25519.PublicKey, len(cfg.Keys))
	for id, key := range cfg.Keys {
		if len(key) != ed25519.PublicKeySize || id != hex.EncodeToString(key) {
			return nil, errors.New("invalid approved Ed25519 key")
		}
		keys[id] = append(ed25519.PublicKey(nil), key...)
	}
	cfg.Keys = keys
	token, err := randomMountID()
	if err != nil {
		return nil, err
	}
	return &import9PGateway{cfg: cfg, host: u.Host, pairingToken: token, pending: make(map[string]*pendingMount), slots: make(chan struct{}, 128)}, nil
}

func loadMountKeys(name string) (map[string]ed25519.PublicKey, error) {
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	keys := make(map[string]ed25519.PublicKey)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		s := strings.TrimSpace(scanner.Text())
		if s == "" || strings.HasPrefix(s, "#") {
			continue
		}
		key, err := hex.DecodeString(s)
		if err != nil || len(key) != ed25519.PublicKeySize {
			return nil, errors.New("approved-key file must contain one 32-byte hex public key per line")
		}
		keys[hex.EncodeToString(key)] = key
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(keys) == 0 {
		return nil, errors.New("approved-key file is empty")
	}
	return keys, nil
}

// Host validation protects the loopback helper against DNS rebinding. A missing
// Origin is rejected, including for non-browser clients.
func (g *import9PGateway) checkOrigin(r *http.Request) bool {
	return r.Host == g.host && r.Header.Get("Origin") == g.cfg.Origin
}

func randomMountID() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

func (g *import9PGateway) authorize(ws *websocket.Conn) error {
	id, err := randomMountID()
	if err != nil {
		return err
	}
	nonce, err := randomMountID()
	if err != nil {
		return err
	}
	deadline := time.Now().Add(g.cfg.ChallengeTTL)
	challenge := mountChallenge{Protocol: mountProtocol, Session: id, Server: g.cfg.Origin, Target: g.cfg.Target, Export: g.cfg.Export, Access: "read-only", User: g.cfg.User, Aname: g.cfg.Aname, Origin: g.cfg.Origin, Nonce: nonce, Expires: deadline.UnixMilli()}
	message, err := json.Marshal(challenge)
	if err != nil {
		return err
	}
	p := &pendingMount{message: message, deadline: deadline}
	g.mu.Lock()
	g.pending[id] = p
	g.mu.Unlock()
	defer func() { g.mu.Lock(); delete(g.pending, id); g.mu.Unlock() }()
	ws.SetReadLimit(4096)
	ws.SetReadDeadline(deadline)
	ws.SetWriteDeadline(deadline)
	if err := ws.WriteJSON(struct {
		Type    string `json:"type"`
		Payload string `json:"payload"`
	}{"mount-challenge", base64.RawURLEncoding.EncodeToString(message)}); err != nil {
		return err
	}
	kind, data, err := ws.ReadMessage()
	if err != nil {
		return err
	}
	if kind != websocket.TextMessage {
		return errors.New("mount proof required before 9P")
	}
	var proof mountProof
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&proof); err != nil {
		return errors.New("invalid mount proof")
	}
	if dec.Decode(new(any)) != io.EOF {
		return errors.New("invalid mount proof")
	}
	// The server retains the exact message; the browser cannot replace its scope.
	key := g.cfg.Keys[proof.PublicKey]
	sig, err := hex.DecodeString(proof.Signature)
	g.mu.Lock()
	defer g.mu.Unlock()
	if err != nil || proof.Type != "mount-proof" || proof.Session != id || p.used || !time.Now().Before(p.deadline) || len(key) != ed25519.PublicKeySize || !ed25519.Verify(key, message, sig) {
		return errors.New("mount authorization denied")
	}
	p.used = true
	return nil
}

// SignHandler only signs the gateway's own pending challenge. It does not take
// message bytes, a key selector, or an upstream address from the browser.
func (g *import9PGateway) SignHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			http.Error(w, "POST required", 405)
			return
		}
		if !g.checkOrigin(r) || r.URL.RawQuery != "" {
			http.Error(w, "origin denied", 403)
			return
		}
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+g.pairingToken)) != 1 {
			http.Error(w, "local pairing required", 403)
			return
		}
		if r.Header.Get("Content-Type") != "application/json" {
			http.Error(w, "JSON required", 415)
			return
		}
		if g.cfg.Signer == nil {
			http.Error(w, "local factotum signing is disabled", 503)
			return
		}
		var request struct {
			Session string `json:"session"`
		}
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 512))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&request); err != nil {
			http.Error(w, "invalid signing request", 400)
			return
		}
		if dec.Decode(new(any)) != io.EOF {
			http.Error(w, "invalid signing request", 400)
			return
		}
		g.mu.Lock()
		p := g.pending[request.Session]
		if p == nil || p.used || p.signing || p.signed || !time.Now().Before(p.deadline) {
			g.mu.Unlock()
			http.Error(w, "challenge unavailable", 403)
			return
		}
		p.signing = true
		message := append([]byte(nil), p.message...)
		deadline := p.deadline
		g.mu.Unlock()
		ctx, cancel := context.WithDeadline(r.Context(), deadline)
		defer cancel()
		key, sig, err := g.cfg.Signer.Sign(ctx, message)
		g.mu.Lock()
		valid := err == nil && len(key) == ed25519.PublicKeySize && g.pending[request.Session] == p && !p.used && time.Now().Before(deadline) && g.cfg.Keys[hex.EncodeToString(key)] != nil && ed25519.Verify(key, message, sig)
		p.signing = false
		p.signed = true // An attempted signing request is never reusable.
		g.mu.Unlock()
		if !valid {
			http.Error(w, "factotum could not authorize this mount", 403)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(mountProof{"mount-proof", request.Session, hex.EncodeToString(key), hex.EncodeToString(sig)})
	})
}

func (g *import9PGateway) deny(ws *websocket.Conn, err error) {
	// Do not disclose key selectors or factotum errors to web clients.
	_ = err
	ws.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.ClosePolicyViolation, "mount authorization or access denied"), time.Now().Add(time.Second))
}

func mountOriginForListen(addr string) (string, error) {
	u, err := url.Parse("http://" + addr)
	if err != nil || u.Hostname() == "" {
		return "", fmt.Errorf("invalid mount listen address %q", addr)
	}
	return u.Scheme + "://" + u.Host, nil
}
