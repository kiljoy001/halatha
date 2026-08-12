//go:build !js && !wasm

package wisp

import (
	"log"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Relay is an http.Handler that speaks the WISP WebSocket→TCP protocol. One
// WebSocket client (one v86 VM) multiplexes many TCP streams over a single
// connection. Mount it at any path; v86 connects to wisp://host:port/ (root).
//
// Egress uses net.Dial directly. The relay is a trusted kernel-resident driver
// (see driver.go); the /net/tcp file service is the user-space network capability
// for other tasks, not the relay's hot path.
type Relay struct {
	// Dial dials the outbound TCP connection for a CONNECT. Defaults to
	// net.Dial("tcp", addr). Override in tests.
	Dial func(network, addr string) (net.Conn, error)

	// Logger receives lifecycle lines (peer connects, dial errors). nil = silent.
	Logger *log.Logger

	// ReadDeadline, if set, is applied to outbound conn reads as a stale
	// stream guard. Zero = block until EOF/error (default, matches v86).
	ReadDeadline time.Duration

	// Auth, if enabled, gates WebSocket upgrades on certificate possession. A
	// client first exchanges a cert + signature over a relay-issued nonce for a
	// single-use token at /.well-known/wisp/auth (see CertAuthHandler), then
	// opens the WebSocket with ?token=<hex>. The relay verifies the cert chains
	// to a configured CA root and the signature matches the cert key. nil or a
	// rootless CertAuth = open relay (local-dev zero-config). When enabled, a
	// bare upgrade (no/invalid token) is rejected with 401 before any TCP is
	// dialed — egress is inert without a valid cert.
	Auth *CertAuth

	upgrader websocket.Upgrader
}

func (r *Relay) defaults() {
	if r.Dial == nil {
		r.Dial = func(network, addr string) (net.Conn, error) {
			return net.Dial(network, addr)
		}
	}
	r.upgrader = websocket.Upgrader{
		ReadBufferSize:  4096,
		WriteBufferSize: 4096,
		// The browser page may be served from a different origin than the relay
		// host (localhost dev vs. a grid relay); accept all, matching
		// cmd/wanix/serve.go's upgrader. Lock down with a token before exposing
		// publicly (see plan risks).
		CheckOrigin: func(*http.Request) bool { return true },
	}
}

// ServeHTTP upgrades the WebSocket and runs one WISP session to completion.
func (r *Relay) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.defaults()
	if r.Auth != nil && r.Auth.Enabled() {
		if !r.Auth.CheckToken(parseHex(req.URL.Query().Get("token"))) {
			http.Error(w, "wisp: unauthorized", http.StatusUnauthorized)
			return
		}
	}
	ws, err := r.upgrader.Upgrade(w, req, nil)
	if err != nil {
		if r.Logger != nil {
			r.Logger.Printf("wisp: upgrade: %v", err)
		}
		return
	}
	r.serveSession(ws, req.RemoteAddr)
}

// maxCredit tops up the client's per-stream congestion counter. The v86 client
// decrements it by one per DATA frame sent and buffers when it hits 0; a value
// this large means the client never congests in practice, and the per-second
// refresher tops it back up. (The client never sends CONTINUE, so server→client
// DATA is ungated.)
const maxCredit uint32 = 0xFFFFFFFF

const readPumpBuf = 32 * 1024

type stream struct {
	id   uint32
	conn net.Conn
	wmu  sync.Mutex // serialize net.Conn.Write from concurrent DATA frames
}

type session struct {
	relay   *Relay
	ws      *websocket.Conn
	peer    string
	streams map[uint32]*stream
	mu      sync.Mutex // guards streams map
	writeMu sync.Mutex // serializes ws.WriteMessage (gorilla: not concurrency-safe)
	done    chan struct{}
	closed  bool
}

func (r *Relay) serveSession(ws *websocket.Conn, peer string) {
	s := &session{
		relay:   r,
		ws:      ws,
		peer:    peer,
		streams: make(map[uint32]*stream),
		done:    make(chan struct{}),
	}
	if r.Logger != nil {
		r.Logger.Printf("wisp: session from %s", peer)
	}
	defer func() {
		s.teardown()
		if r.Logger != nil {
			r.Logger.Printf("wisp: session %s ended", peer)
		}
	}()

	// Critical: send CONTINUE(0) before any client frame. The client's default
	// congestion credit starts at 0; without this it buffers even its CONNECT
	// frames and the session stalls silently (v86/lib.js:3715,3762).
	if !s.send(EncodeContinue(0, maxCredit)) {
		return
	}

	go s.refresher()
	s.readLoop()
}

func (s *session) readLoop() {
	for {
		_, msg, err := s.ws.ReadMessage()
		if err != nil {
			return
		}
		f, err := ReadFrame(msg)
		if err != nil {
			// Malformed frame: skip rather than tear down the whole session.
			if s.relay.Logger != nil {
				s.relay.Logger.Printf("wisp: malformed frame from %s: %v", s.peer, err)
			}
			continue
		}
		s.dispatch(f)
	}
}

func (s *session) dispatch(f Frame) {
	switch f.Type {
	case TypeConnect:
		s.handleConnect(f)
	case TypeData:
		s.handleData(f)
	case TypeClose:
		s.handleClose(f)
	case TypeContinue:
		// Client never sends CONTINUE to the server (v86 has no such path);
		// ignore it if it ever arrives.
	default:
		// Unknown frame type: ignore.
	}
}

func (s *session) handleConnect(f Frame) {
	id := f.StreamID
	proto, port, host, err := ParseConnect(f.Payload)
	if err != nil {
		s.send(EncodeClose(id, CloseNormal))
		return
	}
	if proto != ProtoTCP {
		s.send(EncodeClose(id, CloseNormal))
		return
	}
	addr := net.JoinHostPort(host, strconv.Itoa(int(port)))
	conn, err := s.relay.Dial("tcp", addr)
	if err != nil {
		if s.relay.Logger != nil {
			s.relay.Logger.Printf("wisp: dial %s: %v", addr, err)
		}
		s.send(EncodeClose(id, CloseNormal))
		return
	}
	st := &stream{id: id, conn: conn}
	s.mu.Lock()
	// If a stream with this id already exists (protocol violation), close the
	// old conn first.
	if old := s.streams[id]; old != nil {
		old.conn.Close()
	}
	s.streams[id] = st
	s.mu.Unlock()
	s.send(EncodeContinue(id, maxCredit))
	go s.readPump(st)
}

func (s *session) handleData(f Frame) {
	st := s.stream(f.StreamID)
	if st == nil {
		// DATA for an unknown/closed stream: drop. The client may race a DATA
		// against a just-sent CLOSE.
		return
	}
	st.wmu.Lock()
	_, err := st.conn.Write(f.Payload)
	st.wmu.Unlock()
	if err != nil {
		s.closeStream(f.StreamID, CloseNormal)
	}
}

func (s *session) handleClose(f Frame) {
	// Client-initiated close: tear down the outbound conn. closeStream is
	// idempotent so a following read-pump EOF won't double-send CLOSE.
	s.closeStream(f.StreamID, CloseNormal)
}

// readPump copies outbound→client bytes: each read becomes a DATA frame. On
// EOF/error it closes the stream (sending CLOSE once) and exits.
func (s *session) readPump(st *stream) {
	if s.relay.ReadDeadline != 0 {
		_ = st.conn.SetReadDeadline(time.Now().Add(s.relay.ReadDeadline))
	}
	buf := make([]byte, readPumpBuf)
	for {
		n, err := st.conn.Read(buf)
		if n > 0 {
			if !s.send(EncodeData(st.id, buf[:n])) {
				// session dying; close the conn and bail.
				_ = st.conn.Close()
				return
			}
		}
		if err != nil {
			s.closeStream(st.id, CloseNormal)
			return
		}
	}
}

// refresher tops up each live stream's congestion credit ~once per second so the
// client's decrement-per-DATA counter never wedges at 0. Belt-and-suspenders
// alongside the large initial CONNECT-time CONTINUE.
func (s *session) refresher() {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-t.C:
			for _, id := range s.streamIDs() {
				if !s.send(EncodeContinue(id, maxCredit)) {
					return
				}
			}
		}
	}
}

func (s *session) stream(id uint32) *stream {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.streams[id]
}

func (s *session) streamIDs() []uint32 {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]uint32, 0, len(s.streams))
	for id := range s.streams {
		ids = append(ids, id)
	}
	return ids
}

// closeStream removes the stream and closes its conn exactly once, sending a
// single CLOSE frame. Idempotent: a second caller finds no stream and returns.
func (s *session) closeStream(id uint32, reason uint8) {
	s.mu.Lock()
	st, ok := s.streams[id]
	if !ok {
		s.mu.Unlock()
		return
	}
	delete(s.streams, id)
	s.mu.Unlock()
	_ = st.conn.Close()
	s.send(EncodeClose(id, reason))
}

func (s *session) send(buf []byte) bool {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if s.closed {
		return false
	}
	if err := s.ws.WriteMessage(websocket.BinaryMessage, buf); err != nil {
		s.closed = true
		close(s.done)
		_ = s.ws.Close()
		return false
	}
	return true
}

func (s *session) teardown() {
	s.writeMu.Lock()
	if s.closed {
		s.writeMu.Unlock()
		return
	}
	s.closed = true
	close(s.done)
	s.writeMu.Unlock()

	s.mu.Lock()
	streams := s.streams
	s.streams = nil
	s.mu.Unlock()
	for _, st := range streams {
		_ = st.conn.Close()
	}
	_ = s.ws.Close()
}
