//go:build plan9

// yggmount authenticates with local factotum and mounts a private 9P export.
// A forwarding relay transports TCP; it never receives the signing key.
package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"syscall"
	"time"

	"tractor.dev/wanix/internal/ygg9pauth"
)

type factotumSigner struct{ path, key string }

func (s factotumSigner) rpc(role string, message []byte) ([]byte, error) {
	f, err := os.OpenFile(s.path, os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	call := func(b []byte) ([]byte, error) {
		n, err := f.Write(b)
		if err != nil {
			return nil, err
		}
		if n != len(b) {
			return nil, io.ErrShortWrite
		}
		buf := make([]byte, 4096)
		n, err = f.Read(buf)
		if err != nil {
			return nil, err
		}
		if string(buf[:n]) == "ok" {
			return nil, nil
		}
		if n >= 3 && string(buf[:3]) == "ok " {
			return append([]byte(nil), buf[3:n]...), nil
		}
		return nil, errors.New("factotum refused Ed25519 RPC; check key and protocol support")
	}
	if _, err := call([]byte("start proto=ed25519 role=" + role + " service=halatha-mount pub=" + s.key)); err != nil {
		return nil, err
	}
	if role == "sign" {
		if _, err := call(append([]byte("write "), message...)); err != nil {
			return nil, err
		}
	}
	return call([]byte("read"))
}
func (s factotumSigner) Sign(_ context.Context, b []byte) (ed25519.PublicKey, []byte, error) {
	pub, err := s.rpc("public", nil)
	if err != nil {
		return nil, nil, err
	}
	if hex.EncodeToString(pub) != s.key {
		return nil, nil, errors.New("factotum selected unexpected client key")
	}
	sig, err := s.rpc("sign", b)
	if err != nil {
		return nil, nil, err
	}
	if !ed25519.Verify(pub, b, sig) {
		return nil, nil, errors.New("invalid factotum signature")
	}
	return pub, sig, nil
}
func key(s string) ed25519.PublicKey {
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != 32 || hex.EncodeToString(b) != s {
		log.Fatal("expected canonical 32-byte public key")
	}
	return b
}
func main() {
	generate := flag.Bool("new-identity", false, "generate a new private key directly into local factotum; print public key only")
	ctl := flag.String("factotum-ctl", "/mnt/factotum/ctl", "local factotum control file")
	target := flag.String("target", "", "TCP endpoint reachable through the relay (host:port)")
	server := flag.String("server-key", "", "pinned babyfs public key")
	transport := flag.String("transport-key", "", "pinned relay Yggdrasil public key")
	client := flag.String("client-key", "", "client signing public key")
	root := flag.String("root", "", "exact private export root")
	mountpoint := flag.String("mount", "/n/babyfs", "existing local mountpoint")
	rpc := flag.String("factotum", "/mnt/factotum/rpc", "local factotum RPC file")
	flag.Parse()
	if *generate {
		pub, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			log.Fatal(err)
		}
		// Never emit the private key or pass it through a subprocess/environment.
		command := []byte("key proto=ed25519 service=halatha-mount pub=" + hex.EncodeToString(pub) + " !seed=" + hex.EncodeToString(priv.Seed()))
		f, err := os.OpenFile(*ctl, os.O_WRONLY, 0)
		if err == nil {
			var n int
			n, err = f.Write(command)
			if err == nil && n != len(command) {
				err = io.ErrShortWrite
			}
			f.Close()
		}
		for i := range priv {
			priv[i] = 0
		}
		for i := range command {
			command[i] = 0
		}
		if err != nil {
			log.Fatal("cannot load new identity into factotum; check Ed25519 support")
		}
		fmt.Println(hex.EncodeToString(pub))
		return
	}

	if *target == "" || *root == "" {
		log.Fatal("target and private export root required")
	}
	scope := ygg9pauth.Scope{ServerKey: key(*server), ClientKey: key(*client), TransportKey: key(*transport), Root: *root, Access: "read-write"}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c, err := (&net.Dialer{}).DialContext(ctx, "tcp", *target)
	if err != nil {
		log.Fatal(err)
	}
	defer c.Close()
	// Bound the complete handshake, including a stalled local factotum RPC.
	timer := time.AfterFunc(30*time.Second, func() { os.Exit(124) })
	defer timer.Stop()
	if err := ygg9pauth.Authenticate(ctx, c, scope, factotumSigner{*rpc, *client}); err != nil {
		log.Fatal(err)
	}
	f, err := c.(*net.TCPConn).File()
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()
	// Authenticate clears its deadline; the kernel now negotiates/attaches on
	// that same connection. No extra exporter or reusable credential is posted.
	if err := syscall.Mount(int(f.Fd()), -1, *mountpoint, syscall.MREPL, ""); err != nil {
		log.Fatal(err)
	}
	fmt.Println("Mounted private babyfs export at", *mountpoint)
}
