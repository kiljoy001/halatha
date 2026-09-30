package ygg9pauth

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"9fans.net/go/plan9"
	"net"
)

type Signer interface {
	Sign(context.Context, []byte) (ed25519.PublicKey, []byte, error)
}
type Scope struct {
	ServerKey    ed25519.PublicKey
	ClientKey    ed25519.PublicKey
	TransportKey ed25519.PublicKey
	Root, Access string
}

// Authenticate performs Tauth/read/write on a dedicated auth fid. The proof
// belongs only to conn. The subsequent 9P client may negotiate and attach
// with NOFID on that same connection; no exporter is opened by this helper.
func Authenticate(ctx context.Context, conn net.Conn, expected Scope, signer Signer) error {
	if signer == nil || Address(expected.ServerKey) == nil || expected.Root == "" || (expected.Access != "read-only" && expected.Access != "read-write") {
		return errors.New("pinned server, export, access and signer required")
	}
	deadline := time.Now().Add(30 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err := conn.SetDeadline(deadline); err != nil {
		return err
	}
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	tag := uint16(1)
	rpc := func(f *plan9.Fcall) (*plan9.Fcall, error) {
		f.Tag = tag
		tag++
		if err := plan9.WriteFcall(conn, f); err != nil {
			return nil, err
		}
		r, err := Read(conn)
		if err != nil {
			return nil, err
		}
		if r.Tag != f.Tag || r.Type != f.Type+1 {
			return nil, errors.New("9P authentication refused")
		}
		return r, nil
	}
	v, err := rpc(&plan9.Fcall{Type: plan9.Tversion, Msize: MaxSize, Version: "9P2000"})
	if err != nil {
		return err
	}
	if v.Version != "9P2000" || v.Msize != MaxSize {
		return errors.New("unexpected 9P version")
	}
	aname := ""
	if len(expected.ClientKey) != 0 {
		if len(expected.ClientKey) != ed25519.PublicKeySize || Address(expected.TransportKey) == nil {
			return errors.New("delegated client and pinned transport keys required")
		}
		aname = hex.EncodeToString(expected.ClientKey)
	}
	const fid = plan9.NOFID - 1
	if _, err := rpc(&plan9.Fcall{Type: plan9.Tauth, Afid: fid, Uname: "none", Aname: aname}); err != nil {
		return err
	}
	var message []byte
	for {
		r, err := rpc(&plan9.Fcall{Type: plan9.Tread, Fid: fid, Offset: uint64(len(message)), Count: 1024})
		if err != nil {
			return err
		}
		if len(r.Data) == 0 {
			break
		}
		message = append(message, r.Data...)
		if len(message) > 4096 {
			return errors.New("challenge too large")
		}
	}
	var challenge Challenge
	if err := json.Unmarshal(message, &challenge); err != nil {
		return err
	}
	nonce, err := hex.DecodeString(challenge.Nonce)
	if err != nil || len(nonce) != 32 || challenge.Protocol != Protocol || challenge.ServerKey != hex.EncodeToString(expected.ServerKey) || challenge.Export != expected.Root || challenge.Access != expected.Access || challenge.Expires <= time.Now().UnixMilli() || challenge.Expires > time.Now().Add(31*time.Second).UnixMilli() {
		return errors.New("challenge scope mismatch")
	}
	if aname != "" && Address(expected.TransportKey).String() != challenge.Source {
		return errors.New("challenge transport mismatch")
	}
	pub, sig, err := signer.Sign(ctx, message)
	if err != nil {
		return err
	}
	sourceKey := pub
	if aname != "" {
		if hex.EncodeToString(pub) != aname {
			return errors.New("unexpected client signing key")
		}
		sourceKey = expected.TransportKey
	}
	if Address(sourceKey) == nil || Address(sourceKey).String() != challenge.Source || !ed25519.Verify(pub, message, sig) {
		return errors.New("signing key does not match source node")
	}
	proof, err := json.Marshal(Proof{hex.EncodeToString(pub), hex.EncodeToString(sig)})
	if err != nil {
		return err
	}
	if _, err := rpc(&plan9.Fcall{Type: plan9.Twrite, Fid: fid, Data: proof}); err != nil {
		return err
	}
	if _, err := rpc(&plan9.Fcall{Type: plan9.Tclunk, Fid: fid}); err != nil {
		return err
	}
	return conn.SetDeadline(time.Time{})
}
