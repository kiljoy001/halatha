//go:build !js && !wasm

package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"time"

	"9fans.net/go/plan9"
	"github.com/gorilla/websocket"
	"tractor.dev/wanix/internal/ygg9pauth"
)

// ServeHTTP authenticates this connection before opening the fixed upstream.
func (g *import9PGateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" {
		http.Error(w, "query parameters are not supported", 400)
		return
	}
	if !g.checkOrigin(r) {
		http.Error(w, "origin denied", 403)
		return
	}
	if !websocket.IsWebSocketUpgrade(r) {
		http.Error(w, "WebSocket required", 400)
		return
	}
	select {
	case g.slots <- struct{}{}:
		defer func() { <-g.slots }()
	default:
		http.Error(w, "mount capacity reached", 503)
		return
	}
	upgrade := websocket.Upgrader{CheckOrigin: g.checkOrigin}
	ws, err := upgrade.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer ws.Close()
	if err := g.authorize(ws); err != nil {
		g.deny(ws, err)
		return
	}
	dialer := net.Dialer{Timeout: 5 * time.Second}
	conn, err := dialer.DialContext(r.Context(), "tcp", g.cfg.Target)
	if err != nil {
		ws.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseTryAgainLater, "9P server unavailable"), time.Now().Add(time.Second))
		return
	}
	defer conn.Close()
	if g.cfg.RemoteScope != nil {
		if err := ygg9pauth.Authenticate(r.Context(), conn, *g.cfg.RemoteScope, g.cfg.Signer); err != nil {
			g.deny(ws, errors.New("remote signed attachment denied"))
			return
		}
	}
	if err := ws.WriteJSON(struct {
		Type string `json:"type"`
	}{"mount-ready"}); err != nil {
		return
	}
	ws.SetReadDeadline(time.Time{})
	ws.SetWriteDeadline(time.Time{})
	ws.SetReadLimit(1 << 20)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer conn.Close()
		if err := g.forwardReadOnly9P(ws, conn); err != nil {
			g.deny(ws, err)
		}
	}()
	defer func() { ws.Close(); conn.Close(); <-done }()
	buf := make([]byte, 32*1024)
	for {
		n, err := conn.Read(buf)
		if n > 0 {
			ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := ws.WriteMessage(websocket.BinaryMessage, buf[:n]); err != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

type import9PReader struct {
	ws     *websocket.Conn
	reader io.Reader
}

func (r *import9PReader) Read(p []byte) (int, error) {
	for {
		if r.reader == nil {
			kind, reader, err := r.ws.NextReader()
			if err != nil {
				return 0, err
			}
			if kind != websocket.BinaryMessage {
				return 0, errors.New("binary 9P required")
			}
			r.reader = reader
		}
		n, err := r.reader.Read(p)
		if err == io.EOF {
			r.reader = nil
			if n > 0 {
				return n, nil
			}
			continue
		}
		return n, err
	}
}

// Inspect complete messages, independent of WebSocket frame boundaries. All
// mutation operations and alternate attach identities/exports are denied.
func (g *import9PGateway) forwardReadOnly9P(ws *websocket.Conn, conn net.Conn) error {
	reader := &import9PReader{ws: ws}
	for {
		var header [7]byte
		if _, err := io.ReadFull(reader, header[:]); err != nil {
			return err
		}
		size := binary.LittleEndian.Uint32(header[:4])
		if size < 7 || size > 8192 {
			return errors.New("invalid 9P message size")
		}
		packet := make([]byte, size)
		copy(packet, header[:])
		if _, err := io.ReadFull(reader, packet[7:]); err != nil {
			return err
		}
		call, err := plan9.UnmarshalFcall(packet)
		if err != nil || !g.permit9P(call) {
			return errors.New("9P operation denied")
		}
		conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
		if _, err := io.Copy(conn, bytes.NewReader(packet)); err != nil {
			return err
		}
	}
}

func (g *import9PGateway) permit9P(f *plan9.Fcall) bool {
	if f == nil {
		return false
	}
	switch f.Type {
	case plan9.Tversion:
		return f.Version == "9P2000" && f.Msize >= 256 && f.Msize <= 8192
	case plan9.Tattach:
		return f.Uname == g.cfg.User && f.Aname == g.cfg.Aname && f.Afid == plan9.NOFID
	case plan9.Topen:
		return f.Mode == plan9.OREAD
	case plan9.Twalk, plan9.Tread, plan9.Tclunk, plan9.Tstat, plan9.Tflush:
		return true
	default:
		return false
	}
}
