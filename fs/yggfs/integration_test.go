package yggfs

import (
	"io"
	"io/fs"
	"net"
	"os"
	"strings"
	"testing"
)

// liveSocket returns a reachable Yggdrasil admin socket, or skips.
// Set YGG_ADMIN to point at one; otherwise the well-known paths are tried.
func liveSocket(t *testing.T) string {
	t.Helper()
	candidates := []string{os.Getenv("YGG_ADMIN"), DefaultSocket, "/var/run/yggdrasil.sock"}
	for _, c := range candidates {
		if c == "" {
			continue
		}
		conn, err := net.Dial("unix", c)
		if err == nil {
			conn.Close()
			return c
		}
	}
	t.Skip("no reachable yggdrasil admin socket; set YGG_ADMIN to run")
	return ""
}

// TestAgainstLiveDaemon checks the shape of real output. It asserts
// structure, not specific values, since peers and uptime change.
func TestAgainstLiveDaemon(t *testing.T) {
	fsys := New(liveSocket(t))

	addr := strings.TrimSpace(readLive(t, fsys, "address"))
	// Yggdrasil addresses live in 0200::/7, so they start 02 or 03.
	if !strings.HasPrefix(addr, "2") && !strings.HasPrefix(addr, "3") {
		t.Errorf("address %q does not look like a Yggdrasil address", addr)
	}
	if net.ParseIP(addr) == nil {
		t.Errorf("address %q is not a valid IP", addr)
	}

	status := readLive(t, fsys, "status")
	for _, want := range []string{"yggdrasil", "address", "peers"} {
		if !strings.Contains(status, want) {
			t.Errorf("status missing %q:\n%s", want, status)
		}
	}
	t.Logf("live status:\n%s", status)

	peers := readLive(t, fsys, "peers")
	t.Logf("live peers:\n%s", peers)
	for _, line := range strings.Split(strings.TrimRight(peers, "\n"), "\n") {
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "up ") && !strings.HasPrefix(line, "down") {
			t.Errorf("peer line must start with a state: %q", line)
		}
	}
}

func readLive(t *testing.T, fsys fs.FS, name string) string {
	t.Helper()
	f, err := fsys.Open(name)
	if err != nil {
		t.Fatalf("open %s: %v", name, err)
	}
	defer f.Close()
	b, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}
