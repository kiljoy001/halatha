//go:build !js && !wasm

package main

import (
	"fmt"
	"net"
	"strings"
	"sync/atomic"
	"testing"
)

// echoListener stands in for the host's private 9P listener.
func echoListener(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	return ln
}

// TestGuest9PAliasReachesTheHost proves the alias works at all, so the deny
// tests below cannot pass by simply blocking everything.
func TestGuest9PAliasReachesTheHost(t *testing.T) {
	ln := echoListener(t)
	target := new(atomic.Value)
	target.Store(ln.Addr().String())

	d := buildWispSafeDialer("", "", "", "", false, false, target, nil, nil)
	conn, err := d("tcp", guest9PAlias+":564")
	if err != nil {
		t.Fatalf("alias %q should reach the host 9P listener: %v", guest9PAlias, err)
	}
	conn.Close()
}

// THE security property: the alias must be the ONLY way loopback is
// reachable. A guest that spells out the real address -- even the exact one
// the alias resolves to -- must still be refused, or the alias becomes an
// SSRF bypass rather than a narrow exception.
func TestGuest9PRealAddressStillBlocked(t *testing.T) {
	ln := echoListener(t)
	addr := ln.Addr().String()
	target := new(atomic.Value)
	target.Store(addr)

	d := buildWispSafeDialer("", "", "", "", true, true, target, nil, nil)

	if _, err := d("tcp", addr); err == nil {
		t.Fatalf("guest dialed the 9P listener's real address %s directly; "+
			"the alias must not widen the loopback ban", addr)
	}

	// Other internal targets stay blocked with the alias enabled.
	for _, bad := range []string{
		"127.0.0.1:22",
		"127.0.0.1:5001", // the Kubo API
		"[::1]:564",
		"10.0.0.1:564",
		"169.254.169.254:80",
	} {
		if _, err := d("tcp", bad); err == nil {
			t.Errorf("%s was reachable with the alias enabled", bad)
		}
	}
}

// Near-miss spellings must not resolve. If "host9p:564" worked, a guest
// could append a port and start probing.
func TestGuest9PAliasIsExactMatchOnly(t *testing.T) {
	ln := echoListener(t)
	target := new(atomic.Value)
	target.Store(ln.Addr().String())

	d := buildWispSafeDialer("", "", "", "", true, true, target, nil, nil)

	// The PORT is free -- a WISP CONNECT carries it separately and the
	// guest may name anything; we dial the listener we bound regardless.
	for _, ok := range []string{guest9PAlias + ":564", guest9PAlias + ":1", guest9PAlias + ":65535"} {
		conn, err := d("tcp", ok)
		if err != nil {
			t.Errorf("alias with port %q should resolve: %v", ok, err)
			continue
		}
		conn.Close()
	}

	// Every spelling of the SAME address must resolve, or one form works
	// while another is refused for no reason a caller can see.
	for _, same := range []string{
		"192.0.2.9:564",
		"[::ffff:192.0.2.9]:564", // IPv4-mapped IPv6
	} {
		conn, err := d("tcp", same)
		if err != nil {
			t.Errorf("equivalent spelling %q should resolve: %v", same, err)
			continue
		}
		conn.Close()
	}

	// A DIFFERENT address must not, including near neighbours in the same
	// reserved block.
	for _, variant := range []string{
		"192.0.2.8:564",
		"192.0.2.10:564",
		"192.0.2.90:564",
		"92.0.2.9:564",
		"192.0.2.9.9:564",
	} {
		conn, err := d("tcp", variant)
		if err == nil {
			conn.Close()
			t.Errorf("variant %q resolved; only the exact alias address may work", variant)
		}
	}
}

// With the listener disabled the alias must fail closed, not fall through
// to the normal policy path with a weird address.
func TestGuest9PAliasFailsClosedWhenDisabled(t *testing.T) {
	for name, target := range map[string]*atomic.Value{
		"nil holder":   nil,
		"empty holder": new(atomic.Value),
	} {
		d := buildWispSafeDialer("", "", "", "", true, true, target, nil, nil)
		conn, err := d("tcp", guest9PAlias+":564")
		if err == nil {
			conn.Close()
			t.Errorf("%s: alias resolved while the listener is disabled", name)
			continue
		}
		if !strings.Contains(err.Error(), "not enabled") {
			t.Errorf("%s: want a not-enabled error, got %v", name, err)
		}
	}
}

// The alias must not become an allowlisted peer or otherwise leak into the
// public-peer logic.
func TestGuest9PAliasIsNotAPeer(t *testing.T) {
	ln := echoListener(t)
	target := new(atomic.Value)
	target.Store(ln.Addr().String())

	// defaultPeer set to the alias must not make arbitrary dials work.
	d := buildWispSafeDialer("", guest9PAlias, "", "", false, false, target, nil, nil)
	for _, bad := range []string{"93.184.216.34:443", "127.0.0.1:22"} {
		if conn, err := d("tcp", bad); err == nil {
			conn.Close()
			t.Errorf("%s allowed when the alias was configured as defaultPeer", bad)
		}
	}
}

// A guest must not be able to retarget the alias by racing a value in.
// The holder is written only by the server, but assert the dialer reads it
// rather than caching a copy, so a later rebind takes effect.
func TestGuest9PAliasReadsTargetAtDialTime(t *testing.T) {
	first := echoListener(t)
	second := echoListener(t)

	target := new(atomic.Value)
	target.Store(first.Addr().String())
	d := buildWispSafeDialer("", "", "", "", false, false, target, nil, nil)

	c1, err := d("tcp", guest9PAlias+":564")
	if err != nil {
		t.Fatal(err)
	}
	c1.Close()

	// Close the first listener and repoint; the dialer must follow.
	first.Close()
	target.Store(second.Addr().String())

	c2, err := d("tcp", guest9PAlias+":564")
	if err != nil {
		t.Fatalf("alias did not follow a retarget: %v", err)
	}
	remote := c2.RemoteAddr().String()
	c2.Close()
	if remote != second.Addr().String() {
		t.Errorf("alias dialed %s, want the current target %s", remote, second.Addr().String())
	}
}

// Malformed input on the alias path must not panic the relay.
func TestGuest9PAliasMalformedInput(t *testing.T) {
	ln := echoListener(t)
	target := new(atomic.Value)
	target.Store(ln.Addr().String())
	d := buildWispSafeDialer("", "", "", "", true, true, target, nil, nil)

	for _, addr := range []string{
		"",
		":",
		strings.Repeat("host9p", 100),
		guest9PAlias, // no port: not a valid host:port, must not resolve
		fmt.Sprintf("%s\x00:564", guest9PAlias),
		guest9PAlias + "\n" + guest9PAlias + ":564",
		"192.0.2.9:notaport",
	} {
		conn, err := d("tcp", addr)
		if err == nil {
			conn.Close()
			t.Errorf("malformed %q was accepted", addr)
		}
	}
}
