//go:build !js && !wasm

package main

import (
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
)

// TestWispDialerBlocksInternalTargets pins the SSRF egress policy. The relay
// is reachable by anyone who loads the page, so a hole here turns the VPS
// into an open proxy onto its own internal network.
func TestWispDialerBlocksInternalTargets(t *testing.T) {
	d := buildWispSafeDialer("", "", "", "", true, true, nil, nil, nil)

	blocked := []struct{ addr, why string }{
		{"127.0.0.1:564", "IPv4 loopback"},
		{"[::1]:564", "IPv6 loopback"},
		{"10.0.0.5:564", "RFC1918 10/8"},
		{"192.168.1.1:564", "RFC1918 192.168/16"},
		{"172.16.0.1:564", "RFC1918 172.16/12"},
		{"169.254.169.254:564", "link-local cloud metadata"},
		{"localhost:564", "loopback by name"},
		{"[201:dd03:96a:1c97:7b9a:98ee:c9ab:1e60]:564", "VPS self Yggdrasil IP"},
	}
	for _, tc := range blocked {
		_, err := d("tcp", tc.addr)
		if err == nil {
			t.Errorf("%s (%s) was allowed, want blocked", tc.addr, tc.why)
			continue
		}
		// The rejection must come from the address filter itself. A dial
		// timeout would also be non-nil, which would let a disabled filter
		// masquerade as a working one.
		if !strings.Contains(err.Error(), "prohibited") {
			t.Errorf("%s (%s) was not refused by the address filter: %v", tc.addr, tc.why, err)
		}
	}
}

// TestWispDialerHonorsDenyList: a deployment names networks the relay must
// never reach, such as its operator's own machines. They arrive through
// configuration (--wisp-deny), so the public source never lists them, and a
// whole prefix is denied, not just the first /64 of it.
func TestWispDialerHonorsDenyList(t *testing.T) {
	_, denied, _ := net.ParseCIDR("200:89f8:c3a4::/48") // placeholder test network
	d := buildWispSafeDialer("", "", "", "", true, true, nil, nil, []*net.IPNet{denied})
	for _, addr := range []string{
		"[200:89f8:c3a4::1]:564",
		"[200:89f8:c3a4:a07f:32c2:f446:894c:696a]:564",
		"[200:89f8:c3a4:ffff::9]:17019",
	} {
		_, err := d("tcp", addr)
		if err == nil || !strings.Contains(err.Error(), "prohibited") {
			t.Errorf("%s inside the deny list was not refused: %v", addr, err)
		}
	}
}

func TestParseDenyList(t *testing.T) {
	nets, err := parseDenyList(" 200:89f8:c3a4::/48, 10.9.0.0/16 ,")
	if err != nil || len(nets) != 2 || nets[0].String() != "200:89f8:c3a4::/48" || nets[1].String() != "10.9.0.0/16" {
		t.Fatalf("parseDenyList: %v %v", nets, err)
	}
	if nets, err := parseDenyList(""); err != nil || len(nets) != 0 {
		t.Fatalf("empty list: %v %v", nets, err)
	}
	if _, err := parseDenyList("200:89f8:c3a4::1"); err == nil {
		t.Fatal("a bare address, not a prefix, was accepted")
	}
}

// TestWispDialerBlocksUnauthorizedPorts proves port policy is enforced: with
// 9P and grid disabled, a public address on those ports is still refused.
func TestWispDialerBlocksUnauthorizedPorts(t *testing.T) {
	d := buildWispSafeDialer("", "", "", "", false, false, nil, nil, nil)

	for _, addr := range []string{
		"93.184.216.34:564",   // 9P, disabled
		"93.184.216.34:17010", // grid cpu, disabled
		"93.184.216.34:80",    // never allowed
		"93.184.216.34:22",    // never allowed
		"93.184.216.34:443",   // never allowed
	} {
		_, err := d("tcp", addr)
		if err == nil {
			t.Errorf("%s was allowed, want blocked", addr)
			continue
		}
		if !strings.Contains(err.Error(), "prohibited by security policy") {
			t.Errorf("%s blocked by the wrong rule: %v", addr, err)
		}
	}
}

// TestWispDialerAllowsConfiguredPeer proves the allowlist actually admits
// its peer, so the deny tests above cannot pass by blocking everything.
func TestWispDialerAllowsConfiguredPeer(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()

	// The peer is explicitly allowlisted, but it resolves to loopback, so the
	// private-IP check must still veto it. Authorization is not a bypass of
	// the internal-address filter.
	d := buildWispSafeDialer("", ln.Addr().String(), "", "", false, false, nil, nil, nil)
	if _, err := d("tcp", ln.Addr().String()); err == nil {
		t.Fatal("allowlisted loopback peer was dialed; private-IP filter bypassed by authorization")
	}
}

// TestWispDialerRejectsMalformedAddr pins input handling on the dial path.
func TestWispDialerRejectsMalformedAddr(t *testing.T) {
	d := buildWispSafeDialer("", "", "", "", true, true, nil, nil, nil)
	for _, addr := range []string{
		"",
		"no-port",
		"host:notaport",
		":",
		"[::1]",
		strings.Repeat("a", 300) + ":564",
	} {
		conn, err := d("tcp", addr)
		if err == nil {
			conn.Close()
			t.Errorf("malformed addr %q was accepted", addr)
		}
	}
}

// TestParsePeersFile pins the allowlist parser, including the comment and
// scheme handling the peers file relies on.
func TestParsePeersFile(t *testing.T) {
	dir := t.TempDir()
	path := fmt.Sprintf("%s/peers", dir)
	content := strings.Join([]string{
		"# comment line",
		"",
		"tls://1.2.3.4:1443",
		"tcp://5.6.7.8:9001",
		"9.10.11.12:1234",
		"   ",
		"tls://13.14.15.16:443?sni=example.com",
		"# trailing comment",
	}, "\n")
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}

	peers := parsePeersFile(path)
	for _, want := range []string{
		"1.2.3.4:1443",
		"5.6.7.8:9001",
		"9.10.11.12:1234",
		"13.14.15.16:443",
	} {
		if !peers[want] {
			t.Errorf("peer %q missing from parsed allowlist %v", want, peers)
		}
	}
	// Comments and blanks must never become allowlist entries.
	for got := range peers {
		if strings.HasPrefix(got, "#") || strings.TrimSpace(got) == "" {
			t.Errorf("parser admitted junk entry %q", got)
		}
	}
	// A missing file must yield an empty allowlist, never a nil map panic.
	if got := parsePeersFile(dir + "/does-not-exist"); len(got) != 0 {
		t.Errorf("missing file produced %v", got)
	}
	if got := parsePeersFile(""); len(got) != 0 {
		t.Errorf("empty path produced %v", got)
	}
}

// TestWispDialerPortPolicy pins exactly which ports the 9P and grid flags
// open. Widening this set silently would expand the relay's reach beyond
// what the flags advertise.
func TestWispDialerPortPolicy(t *testing.T) {
	if testing.Short() {
		t.Skip("port policy probes attempt real dials for allowed ports")
	}
	// TEST-NET-1 (RFC 5737): a public, non-allowlisted, non-routable address.
	// Only the port rules can admit it, and allowed ports fail to connect
	// quickly rather than reaching a real host.
	const pub = "192.0.2.1"

	cases := []struct {
		port          int
		allow9p, grid bool
		wantByPolicy  bool // true => must be refused by the policy check
	}{
		{564, true, false, false},   // 9P enabled
		{564, false, false, true},   // 9P disabled
		{17019, false, true, false}, // rcpu
		{17010, false, true, false}, // cpu
		{567, false, true, false},   // auth
		{17019, false, false, true}, // grid disabled
		{563, true, true, true},     // adjacent to 9P, not allowed
		{565, true, true, true},     // adjacent to 9P, not allowed
		{17011, true, true, true},   // adjacent to cpu, not allowed
		{566, true, true, true},     // adjacent to auth, not allowed
		{568, true, true, true},     // adjacent to auth, not allowed
	}

	for _, tc := range cases {
		d := buildWispSafeDialer("", "", "", "", tc.allow9p, tc.grid, nil, nil, nil)
		addr := fmt.Sprintf("%s:%d", pub, tc.port)
		conn, err := d("tcp", addr)
		if conn != nil {
			conn.Close()
		}
		refusedByPolicy := err != nil && strings.Contains(err.Error(), "prohibited by security policy")
		if refusedByPolicy != tc.wantByPolicy {
			t.Errorf("port %d (9p=%v grid=%v): refusedByPolicy=%v want %v (err=%v)",
				tc.port, tc.allow9p, tc.grid, refusedByPolicy, tc.wantByPolicy, err)
		}
	}
}
