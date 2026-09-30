// Package ygg9pauth gates private 9P exports with a connection-bound,
// Ed25519 signed challenge and a Yggdrasil node-key allowlist.
package ygg9pauth

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"9fans.net/go/plan9"
)

const Protocol = "halatha-ygg9p-v1"
const MaxSize = 8192

type Challenge struct {
	Protocol  string `json:"protocol"`
	ServerKey string `json:"serverKey"`
	Export    string `json:"export"`
	Access    string `json:"access"`
	Source    string `json:"source"`
	Nonce     string `json:"nonce"`
	Expires   int64  `json:"expires"`
}

type Proof struct {
	PublicKey string `json:"publicKey"`
	Signature string `json:"signature"`
}

type Export struct {
	Root          string
	Access        string
	TransportKeys []ed25519.PublicKey
	Open          func() (io.ReadWriteCloser, error)
}

type Config struct {
	ServerKey ed25519.PublicKey
	Keys      map[string]ed25519.PublicKey
	Exports   map[string]Export
}

// Address implements Yggdrasil's current AddrForKey algorithm: inverse key,
// leading-one count, then the next 112 bits. It is not a hash of the key.
// See yggdrasil-go/src/address/address.go.
func Address(key ed25519.PublicKey) net.IP {
	if len(key) != ed25519.PublicKeySize {
		return nil
	}
	ip := make(net.IP, 16)
	ip[0] = 2
	leading, start := 0, 0
	for start < 256 && key[start/8]&(0x80>>uint(start%8)) == 0 {
		leading++
		start++
	}
	if leading > 127 {
		return nil
	}
	ip[1] = byte(leading)
	start++ // skip first zero bit of the inverted key
	for i := 0; i < 112 && start+i < 256; i++ {
		if key[(start+i)/8]&(0x80>>uint((start+i)%8)) == 0 {
			ip[2+i/8] |= 0x80 >> uint(i%8)
		}
	}
	return ip
}

func (c Config) Validate() error {
	if Address(c.ServerKey) == nil {
		return errors.New("server key, dedicated absolute export root, exporter and approved keys required")
	}
	for id, key := range c.Keys {
		if Address(key) == nil || id != hex.EncodeToString(key) {
			return errors.New("invalid approved Yggdrasil key")
		}
		e, ok := c.Exports[id]
		if !ok || e.Root == "" || e.Root == "/" || e.Root[0] != '/' || filepath.Clean(e.Root) != e.Root || e.Open == nil || (e.Access != "read-only" && e.Access != "read-write") {
			return errors.New("each key requires a dedicated export and explicit access mode")
		}
		for _, transport := range e.TransportKeys {
			if Address(transport) == nil {
				return errors.New("invalid transport key")
			}
		}
		for other, k := range c.Keys {
			if other != id && (Address(k).Equal(Address(key)) || c.Exports[other].Root == e.Root || strings.HasPrefix(e.Root, c.Exports[other].Root+"/") || strings.HasPrefix(c.Exports[other].Root, e.Root+"/")) {
				return errors.New("duplicate node address or overlapping exports")
			}
		}
	}
	return nil
}

// An omitted transport list preserves direct node-key authentication. An explicit
// list authorizes only those forwarding nodes, independently of client identity.
func transportAllowed(cfg Config, id string, peer net.IP) bool {
	e, ok := cfg.Exports[id]
	if !ok {
		return false
	}
	keys := e.TransportKeys
	if len(keys) == 0 {
		keys = []ed25519.PublicKey{cfg.Keys[id]}
	}
	for _, k := range keys {
		if Address(k).Equal(peer) {
			return true
		}
	}
	return false
}

func Read(r io.Reader) (*plan9.Fcall, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, err
	}
	n := binary.LittleEndian.Uint32(hdr[:])
	if n < 7 || n > MaxSize {
		return nil, errors.New("invalid 9P frame size")
	}
	b := make([]byte, n)
	copy(b, hdr[:])
	if _, err := io.ReadFull(r, b[4:]); err != nil {
		return nil, err
	}
	return plan9.UnmarshalFcall(b)
}

// Serve checks the peer's node address as well as proof of its private key.
// The caller must bind to Address(ServerKey); an address check alone does not
// establish packet ingress through a particular host interface.
func Serve(conn net.Conn, peer net.IP, cfg Config) error {
	defer conn.Close()
	if err := cfg.Validate(); err != nil {
		return err
	}
	peer = peer.To16()
	if peer == nil || peer[0] != 2 {
		return errors.New("Yggdrasil node source required")
	}
	allowed := false
	for id := range cfg.Keys {
		if transportAllowed(cfg, id, peer) {
			allowed = true
			break
		}
	}
	if !allowed {
		return errors.New("unapproved Yggdrasil transport")
	}
	var selected string
	for id, key := range cfg.Keys {
		if Address(key).Equal(peer) && transportAllowed(cfg, id, peer) {
			selected = id
			break
		}
	}
	var export Export
	deadline := time.Now().Add(30 * time.Second)
	if err := conn.SetDeadline(deadline); err != nil {
		return err
	}
	var nonce [32]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	var message []byte
	authfid := uint32(plan9.NOFID)
	verified, attempted := false, false
	msize := uint32(MaxSize)
	for {
		f, err := Read(conn)
		if err != nil {
			return err
		}
		r := &plan9.Fcall{Type: f.Type + 1, Tag: f.Tag}
		deny := func(reason string) error {
			return plan9.WriteFcall(conn, &plan9.Fcall{Type: plan9.Rerror, Tag: f.Tag, Ename: reason})
		}
		switch f.Type {
		case plan9.Tversion:
			if f.Version != "9P2000" || f.Msize < 256 {
				deny("9P2000 required")
				return errors.New("unsupported version")
			}
			msize = min(f.Msize, uint32(MaxSize))
			r.Msize = msize
			r.Version = "9P2000"
		case plan9.Tauth:
			if authfid != plan9.NOFID || f.Afid == plan9.NOFID || f.Uname != "none" {
				deny("invalid auth request")
				return errors.New("invalid auth request")
			}
			if f.Aname != "" {
				selected = f.Aname
			}
			if _, ok := cfg.Keys[selected]; !ok || !transportAllowed(cfg, selected, peer) {
				deny("unapproved client or transport")
				return errors.New("unapproved client or transport")
			}
			export = cfg.Exports[selected]
			message, err = json.Marshal(Challenge{Protocol, hex.EncodeToString(cfg.ServerKey), export.Root, export.Access, peer.String(), hex.EncodeToString(nonce[:]), deadline.UnixMilli()})
			if err != nil {
				return err
			}
			authfid = f.Afid
			r.Aqid = plan9.Qid{Type: plan9.QTAUTH, Path: 1}
		case plan9.Tread:
			if f.Fid != authfid || authfid == plan9.NOFID || f.Offset > uint64(len(message)) {
				deny("auth fid required")
				return errors.New("auth fid required")
			}
			end := min(uint64(len(message)), f.Offset+uint64(min(f.Count, msize-11)))
			r.Data = message[f.Offset:end]
		case plan9.Twrite:
			if attempted || f.Fid != authfid || authfid == plan9.NOFID || f.Offset != 0 {
				deny("one proof required")
				return errors.New("invalid proof request")
			}
			attempted = true
			var proof Proof
			if err := json.Unmarshal(f.Data, &proof); err != nil {
				deny("invalid proof")
				return err
			}
			key := cfg.Keys[proof.PublicKey]
			sig, err := hex.DecodeString(proof.Signature)
			if proof.PublicKey != selected || err != nil || time.Now().After(deadline) || !transportAllowed(cfg, selected, peer) || !ed25519.Verify(key, message, sig) {
				deny("mount authentication denied")
				return errors.New("mount authentication denied")
			}
			verified = true
			r.Count = uint32(len(f.Data))
		case plan9.Tattach:
			if !verified || time.Now().After(deadline) || f.Uname != "none" || f.Aname != "" || (f.Afid != plan9.NOFID && f.Afid != authfid) || f.Fid == authfid {
				deny("signed authentication required")
				return errors.New("unauthenticated attach")
			}
			upstream, err := export.Open()
			if err != nil {
				deny("export unavailable")
				return err
			}
			defer upstream.Close()
			if err := plan9.WriteFcall(upstream, &plan9.Fcall{Type: plan9.Tversion, Tag: plan9.NOTAG, Msize: msize, Version: "9P2000"}); err != nil {
				return err
			}
			v, err := Read(upstream)
			if err != nil || v.Type != plan9.Rversion || v.Version != "9P2000" || v.Msize < msize {
				return errors.New("invalid export version")
			}
			f.Afid = plan9.NOFID
			if err := plan9.WriteFcall(upstream, f); err != nil {
				return err
			}
			if err := conn.SetDeadline(time.Time{}); err != nil {
				return err
			}
			return proxy(conn, upstream, authfid, export.Access)
		case plan9.Tclunk:
			if f.Fid != authfid || authfid == plan9.NOFID {
				deny("invalid auth fid")
				return errors.New("invalid auth fid")
			}
			// Keep the proof bound to this connection for the browser's NOFID attach.
		default:
			deny("authentication required")
			return fmt.Errorf("unauthorized operation %d", f.Type)
		}
		if err := plan9.WriteFcall(conn, r); err != nil {
			return err
		}
	}
}

func proxy(conn net.Conn, upstream io.ReadWriteCloser, authfid uint32, access string) error {
	done := make(chan error, 1)
	var writer sync.Mutex
	reply := func(f *plan9.Fcall) error { writer.Lock(); defer writer.Unlock(); return plan9.WriteFcall(conn, f) }
	go func() {
		for {
			f, err := Read(upstream)
			if err != nil {
				conn.Close()
				done <- err
				return
			}
			if err := reply(f); err != nil {
				conn.Close()
				done <- err
				return
			}
		}
	}()
	reserved := true
	defer func() { upstream.Close(); conn.Close(); <-done }()
	for {
		f, err := Read(conn)
		if err != nil {
			return err
		}
		allowed := false
		switch f.Type {
		case plan9.Topen:
			allowed = access == "read-write" || f.Mode == plan9.OREAD
		case plan9.Twrite, plan9.Tcreate, plan9.Tremove, plan9.Twstat:
			allowed = access == "read-write"
		case plan9.Twalk, plan9.Tread, plan9.Tstat, plan9.Tclunk, plan9.Tflush:
			allowed = true
		}
		if reserved && f.Type == plan9.Tclunk && f.Fid == authfid {
			reserved = false
			if err := reply(&plan9.Fcall{Type: plan9.Rclunk, Tag: f.Tag}); err != nil {
				return err
			}
			continue
		}
		if reserved && (f.Fid == authfid || (f.Type == plan9.Twalk && f.Newfid == authfid)) {
			allowed = false
		}
		if !allowed {
			return errors.New("export operation denied")
		}
		if err := plan9.WriteFcall(upstream, f); err != nil {
			return err
		}
	}
}
