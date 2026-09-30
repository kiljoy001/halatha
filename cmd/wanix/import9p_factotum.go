//go:build !js && !wasm

package main

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"9fans.net/go/plan9"
	"9fans.net/go/plan9/client"
)

type factotumMountSigner struct{ socket, user, service string }

func (s factotumMountSigner) Sign(ctx context.Context, message []byte) (ed25519.PublicKey, []byte, error) {
	if !safeFactotumService(s.service) {
		return nil, nil, errors.New("invalid factotum service selector")
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", s.socket)
	if err != nil {
		return nil, nil, err
	}
	defer conn.Close()
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(30 * time.Second)
	}
	conn.SetDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	c, err := client.NewConn(conn)
	if err != nil {
		return nil, nil, err
	}
	fs, err := c.Attach(nil, s.user, "")
	if err != nil {
		return nil, nil, err
	}
	key, err := factotumMountRPC(fs, s.service, "public", nil)
	if err != nil {
		return nil, nil, err
	}
	if len(key) != ed25519.PublicKeySize {
		return nil, nil, errors.New("invalid factotum public key length")
	}
	sig, err := factotumMountRPC(fs, s.service, "sign", message)
	if err != nil {
		return nil, nil, err
	}
	if !ed25519.Verify(key, message, sig) {
		return nil, nil, errors.New("factotum signature does not match selected public key")
	}
	return ed25519.PublicKey(key), sig, nil
}

func safeFactotumService(s string) bool {
	if s == "" || len(s) > 128 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("._-", c)) {
			return false
		}
	}
	return true
}

func factotumMountRPC(fs *client.Fsys, service, role string, message []byte) ([]byte, error) {
	fid, err := fs.Open("rpc", plan9.ORDWR)
	if err != nil {
		return nil, err
	}
	defer fid.Close()
	call := func(request []byte) ([]byte, error) {
		n, err := fid.Write(request)
		if err != nil {
			return nil, err
		}
		if n != len(request) {
			return nil, io.ErrShortWrite
		}
		buf := make([]byte, 4096)
		n, err = fid.Read(buf)
		if err != nil {
			return nil, err
		}
		if string(buf[:n]) == "ok" {
			return nil, nil
		}
		if n >= 3 && string(buf[:3]) == "ok " {
			return append([]byte(nil), buf[3:n]...), nil
		}
		return nil, errors.New("factotum RPC refused")
	}
	if _, err := call([]byte(fmt.Sprintf("start proto=ed25519 role=%s service=%s", role, service))); err != nil {
		return nil, err
	}
	if role == "sign" {
		if _, err := call(append([]byte("write "), message...)); err != nil {
			return nil, err
		}
	}
	return call([]byte("read"))
}
