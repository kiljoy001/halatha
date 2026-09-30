//go:build !js && !wasm

package main

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"9fans.net/go/plan9"
	"9fans.net/go/plan9/client"
	"tractor.dev/wanix/fs"
	"tractor.dev/wanix/fs/p9p"
)

// Use an isolated test factotum, never a user's normal credential agent. Only
// ephemeral fixture keys are inserted, and their unique selectors are deleted.
func testFactotumSigner(t *testing.T, key ed25519.PrivateKey) factotumMountSigner {
	t.Helper()
	socket := os.Getenv("HALATHA_TEST_FACTOTUM_SOCKET")
	if socket == "" {
		t.Skip("set HALATHA_TEST_FACTOTUM_SOCKET to an isolated Ed25519-enabled factotum")
	}
	user := os.Getenv("USER")
	if user == "" {
		t.Fatal("USER is required")
	}
	service := "halatha-test-" + hex.EncodeToString(key.Public().(ed25519.PublicKey))[:16]
	ctl := func(command string) error {
		conn, err := net.DialTimeout("unix", socket, 3*time.Second)
		if err != nil {
			return err
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(3 * time.Second))
		c, err := client.NewConn(conn)
		if err != nil {
			return err
		}
		mounted, err := c.Attach(nil, user, "")
		if err != nil {
			return err
		}
		fid, err := mounted.Open("ctl", plan9.OWRITE)
		if err != nil {
			return err
		}
		defer fid.Close()
		n, err := fid.Write([]byte(command))
		if err == nil && n != len(command) {
			err = fmt.Errorf("short ctl write")
		}
		return err
	}
	if err := ctl("key proto=ed25519 service=" + service + " !sk=" + hex.EncodeToString(key)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := ctl("delkey proto=ed25519 service=" + service); err != nil {
			t.Logf("delete ephemeral key: %v", err)
		}
	})
	return factotumMountSigner{socket: socket, user: user, service: service}
}
func TestImport9PFactotumIntegration(t *testing.T) {
	f := newMountFixture(t, 0)
	signer := testFactotumSigner(t, f.key)
	f.gateway.cfg.Signer = signer
	ws, c, _ := f.connect(t)
	status, proof := f.signRequest(t, fmtSession(c.Session), f.server.URL)
	if status != 200 {
		t.Fatalf("real factotum signer: HTTP %d", status)
	}
	readyMount(t, ws, proof)
	mounted, err := p9p.ClientFS(&websocketStream{conn: ws}, "none", "")
	if err != nil {
		t.Fatal(err)
	}
	data, err := fs.ReadFile(mounted, "hello")
	if err != nil || string(data) != "network fixture" {
		t.Fatalf("real factotum mount read %q %v", data, err)
	}
	// Factotum connection cancellation must not leave RPC waiting indefinitely.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := signer.Sign(ctx, []byte("cancelled")); err == nil {
		t.Fatal("cancelled signing succeeded")
	}
}
func TestImport9PBrowserFactotumIntegration(t *testing.T) {
	if os.Getenv("HALATHA_TEST_MOUNT_BROWSER") != "1" {
		t.Skip("set HALATHA_TEST_MOUNT_BROWSER=1 after building browser assets")
	}
	f := newMountFixture(t, 0)
	f.gateway.cfg.Signer = testFactotumSigner(t, f.key)
	read, expected := "hello", "network fixture"
	if target := os.Getenv("HALATHA_TEST_MOUNT_TARGET"); target != "" {
		if _, _, err := net.SplitHostPort(target); err != nil {
			t.Fatal(err)
		}
		f.gateway.cfg.Target = target
		read = os.Getenv("HALATHA_TEST_MOUNT_READ")
		expected = os.Getenv("HALATHA_TEST_MOUNT_EXPECT")
		if read == "" {
			t.Fatal("HALATHA_TEST_MOUNT_READ is required for a remote target")
		}
	}
	f.server.Config.Handler.(*http.ServeMux).Handle("/", http.FileServer(http.Dir("../..")))
	args := []string{"dev/test-network-9p-e2e.mjs", f.server.URL + "/examples/network-9p.html", "--read=" + read, "--expect=" + expected}
	if hash := os.Getenv("HALATHA_TEST_MOUNT_SHA256"); hash != "" {
		args = append(args, "--sha256="+hash)
	}
	cmd := exec.Command("node", args...)
	cmd.Dir = "../.."
	cmd.Env = append(os.Environ(), "HALATHA_TEST_MOUNT_PAIRING="+f.gateway.pairingToken)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("real browser/factotum mount: %v\n%s", err, out)
	}
	t.Log(strings.TrimSpace(string(out)))
}
