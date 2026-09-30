//go:build plan9

package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"9fans.net/go/plan9"
	"9fans.net/go/plan9/client"
	"tractor.dev/wanix/internal/ygg9pauth"
)

type testSigner ed25519.PrivateKey

func (k testSigner) Sign(_ context.Context, b []byte) (ed25519.PublicKey, []byte, error) {
	p := ed25519.PrivateKey(k)
	return p.Public().(ed25519.PublicKey), ed25519.Sign(p, b), nil
}

func TestNativePrivateQuota(t *testing.T) {
	if os.Getenv("HALATHA_NATIVE_QUOTA_TEST") != "1" {
		t.Skip("explicit native fixture opt-in required")
	}
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	id := hex.EncodeToString(pub)
	dir := t.TempDir()
	image := filepath.Join(dir, "quota.img")
	f, err := os.Create(image)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(8 << 20); err != nil {
		t.Fatal(err)
	}
	f.Close()
	svc := "ygg9p-" + id
	cmd := exec.Command("/bin/hjfs", "-r", "-m", "4", "-n", svc, "-f", image)
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		f, err := os.OpenFile("/srv/"+svc+".cmd", os.O_WRONLY, 0)
		if err == nil {
			f.WriteString("halt\n")
			f.Close()
		}
	})
	mnt := filepath.Join(dir, "mnt")
	os.Mkdir(mnt, 0700)
	f, err = os.OpenFile("/srv/"+svc+".cmd", os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("newuser scott\ncreate /data scott scott 777 d\nsync\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	// The native console processes initialization asynchronously.
	time.Sleep(100 * time.Millisecond)
	e := exportConfig{PublicKey: id, Root: filepath.Join(mnt, "data"), Access: "read-write"}
	cfg := ygg9pauth.Config{ServerKey: pub, Keys: map[string]ed25519.PublicKey{id: pub}, Exports: map[string]ygg9pauth.Export{id: {Root: e.Root, Access: e.Access, Open: func() (io.ReadWriteCloser, error) { return startExport(e) }}}}
	a, b := net.Pipe()
	done := make(chan error, 1)
	go func() { done <- ygg9pauth.Serve(b, ygg9pauth.Address(pub), cfg) }()
	defer func() { a.Close(); <-done }()
	if err := ygg9pauth.Authenticate(context.Background(), a, ygg9pauth.Scope{ServerKey: pub, Root: e.Root, Access: e.Access}, testSigner(key)); err != nil {
		t.Fatal(err)
	}
	c, err := client.NewConn(a)
	if err != nil {
		t.Fatal(err)
	}
	fs, err := c.Attach(nil, "none", "")
	if err != nil {
		t.Fatal(err)
	}
	w, err := fs.Create("work.txt", plan9.ORDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("saved through authenticated attach")); err != nil {
		t.Fatal(err)
	}
	w.Close()
	r, err := fs.Open("work.txt", plan9.OREAD)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(r)
	r.Close()
	if err != nil || string(data) != "saved through authenticated attach" {
		t.Fatal("readback failed", err)
	}
	if _, err := fs.Open("../adm/users", plan9.OREAD); err == nil {
		t.Fatal("escaped export root")
	}
	w, err = fs.Create("fill", plan9.OWRITE, 0600)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 8192-24)
	if _, err := rand.Read(buf); err != nil {
		t.Fatal(err)
	}
	written := 0
	limited := false
	for written < 12<<20 {
		n, err := w.WriteAt(buf, int64(written))
		written += n
		if err != nil {
			limited = true
			break
		}
		if n == 0 {
			t.Fatal("zero-progress write")
		}
	}
	w.Close()
	if !limited {
		t.Fatal("fixed-size filesystem did not stop writes")
	}
	st, err := os.Stat(image)
	if err != nil || st.Size() != 8<<20 {
		t.Fatal("quota image grew", err)
	}
	t.Logf("authenticated write/read, traversal denial and hard quota passed; stopped after %d bytes", written)
}
