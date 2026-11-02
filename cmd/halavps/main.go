// Command halavps serves a per-connection Halatha VPS session namespace.
//
// The guest boots from an immutable GEFS image supplied as a multiboot
// module, so the server does not store the image at all. Each session is
// just the writable side:
//
//	ram/    in-memory filesystem for /home, /tmp, /var and /n
//	host/   browser service tree (clip, downloads, fetch, uploads, usb, ...)
//
// The namespace is exported as 9P2000 over WebSocket for <wanix-bind
// type="import">. Nothing here persists: when the connection ends the ramfs
// and the browser service tree are discarded. User data leaves through the
// browser (save picker or download).
package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"tractor.dev/wanix/fs"
	"tractor.dev/wanix/fs/fskit"
	"tractor.dev/wanix/fs/memfs"
	"tractor.dev/wanix/fs/p9p"
)

var (
	addr = flag.String("addr", "127.0.0.1:8099", "listen address")
	ttl  = flag.Duration("token-ttl", 5*time.Minute, "lifetime of a session token")
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	flag.Parse()

	srv := &server{tokens: newTokenStore(*ttl)}
	srv.wisp = newWispServer(srv)
	log.Printf("halavps: addr=%s token-ttl=%s", *addr, *ttl)
	log.Fatal(http.ListenAndServe(*addr, srv.mux()))
}

var upgrader = websocket.Upgrader{
	ReadBufferSize:  32 << 10,
	WriteBufferSize: 32 << 10,
	CheckOrigin:     func(*http.Request) bool { return true },
}

type server struct {
	tokens *tokenStore
	wisp   *wispServer
}

func (s *server) mux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "ok\n")
	})
	mux.HandleFunc("/token", s.tokenHandler)
	mux.HandleFunc("/vps-token", s.tokenHandler)
	mux.HandleFunc("/session", s.sessionHandler)
	mux.HandleFunc("/net", s.netHandler)
	return mux
}

func (s *server) tokenHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	token, err := s.tokens.issue()
	if err != nil {
		http.Error(w, "token generation failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"token": token})
}

func (s *server) sessionHandler(w http.ResponseWriter, r *http.Request) {
	if !s.tokens.consume(r.URL.Query().Get("token")) {
		http.Error(w, "invalid or expired session token", http.StatusForbidden)
		return
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	conn.SetReadLimit(1 << 20)

	session, err := newSession()
	if err != nil {
		log.Printf("halavps: session setup: %v", err)
		conn.WriteControl(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseInternalServerErr, "session setup failed"),
			time.Now().Add(time.Second))
		return
	}

	stream := &wsStream{conn: conn}
	log.Printf("halavps: session start %s", conn.RemoteAddr())
	if err := p9p.Serve(session, stream, stream); err != nil && !isClosed(err) {
		log.Printf("halavps: session %s: %v", conn.RemoteAddr(), err)
	}
	log.Printf("halavps: session end %s", conn.RemoteAddr())
}

func (s *server) netHandler(w http.ResponseWriter, r *http.Request) {
	if !s.tokens.consume(r.URL.Query().Get("token")) {
		http.Error(w, "invalid or expired session token", http.StatusForbidden)
		return
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	s.wisp.handler(conn)
}

func newSession() (fs.FS, error) {
	// The data plane is client-side: the page binds local ramfs over both
	// ram/ and host/. The server only supplies the clean root mount points
	// and the per-session token.
	return fskit.MapFS{
		"ram":  memfs.New(),
		"host": memfs.New(),
	}, nil
}

func isClosed(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrClosedPipe) ||
		websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway, websocket.CloseNoStatusReceived)
}

// wsStream adapts a binary WebSocket connection to the byte stream p9p expects.
type wsStream struct {
	conn *websocket.Conn
	mu   sync.Mutex
	r    io.Reader
}

func (s *wsStream) Read(p []byte) (int, error) {
	for {
		if s.r == nil {
			kind, r, err := s.conn.NextReader()
			if err != nil {
				return 0, err
			}
			if kind != websocket.BinaryMessage {
				continue
			}
			s.r = r
		}
		n, err := s.r.Read(p)
		if err == io.EOF {
			s.r = nil
			if n > 0 {
				return n, nil
			}
			continue
		}
		return n, err
	}
}

func (s *wsStream) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.conn.WriteMessage(websocket.BinaryMessage, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (s *wsStream) Close() error { return s.conn.Close() }

// tokenStore issues single-use, short-lived tokens. A token is created when
// the page loads and consumed by exactly one WebSocket upgrade; it cannot be
// replayed or shared by a second browser tab.
type tokenStore struct {
	mu  sync.Mutex
	ttl time.Duration
	m   map[string]time.Time
}

func newTokenStore(ttl time.Duration) *tokenStore {
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	return &tokenStore{ttl: ttl, m: make(map[string]time.Time)}
}

func (t *tokenStore) issue() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	tok := hex.EncodeToString(b[:])
	now := time.Now()
	t.mu.Lock()
	for k, exp := range t.m {
		if now.After(exp) {
			delete(t.m, k)
		}
	}
	t.m[tok] = now.Add(t.ttl)
	t.mu.Unlock()
	return tok, nil
}

func (t *tokenStore) consume(tok string) bool {
	if tok == "" {
		return false
	}
	now := time.Now()
	t.mu.Lock()
	defer t.mu.Unlock()
	exp, ok := t.m[tok]
	if !ok || now.After(exp) {
		delete(t.m, tok)
		return false
	}
	delete(t.m, tok)
	return true
}
