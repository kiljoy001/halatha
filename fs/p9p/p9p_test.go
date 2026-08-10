package p9p

import (
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/knusbaum/go9p/client"
	"github.com/knusbaum/go9p/proto"

	"tractor.dev/wanix/fs"
	"tractor.dev/wanix/fs/memfs"
)

func startTestServer(t *testing.T, fsys fs.FS) *client.Client {
	a, b := net.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- Serve(fsys, a, a)
	}()
	t.Cleanup(func() {
		b.Close()
		a.Close()
		select {
		case err := <-done:
			if err != nil && err != io.EOF && !strings.Contains(err.Error(), "closed pipe") {
				t.Errorf("server ended: %v", err)
			}
		case <-time.After(time.Second):
			t.Errorf("server did not exit")
		}
	})
	c, err := client.NewClient(b, "glenda", "")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return c
}

func TestStatAndReaddir(t *testing.T) {
	fsys := memfs.New()
	if f, err := fs.Create(fsys, "hello.txt"); err == nil {
		fs.Write(f, []byte("hello 9front"))
		f.Close()
	} else {
		t.Fatal(err)
	}
	fs.Mkdir(fsys, "sub", 0755)

	c := startTestServer(t, fsys)

	st, err := c.Stat("/")
	if err != nil {
		t.Fatalf("Stat /: %v", err)
	}
	if st.Name != "/" {
		t.Errorf("root name = %q, want /", st.Name)
	}
	if st.Mode&proto.DMDIR == 0 {
		t.Errorf("root not a directory: mode=%o", st.Mode)
	}

	entries, err := c.Readdir("/")
	if err != nil {
		t.Fatalf("Readdir /: %v", err)
	}
	names := map[string]bool{}
	for _, e := range entries {
		names[e.Name] = true
	}
	if !names["hello.txt"] || !names["sub"] {
		t.Errorf("readdir missing entries: %v", names)
	}
}

func TestOpenRead(t *testing.T) {
	fsys := memfs.New()
	if f, err := fs.Create(fsys, "hello.txt"); err == nil {
		fs.Write(f, []byte("hello 9front"))
		f.Close()
	} else {
		t.Fatal(err)
	}

	c := startTestServer(t, fsys)

	f, err := c.Open("/hello.txt", proto.Oread)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer f.Close()
	buf := make([]byte, 64)
	n, err := f.Read(buf)
	if err != nil && err != io.EOF {
		t.Fatalf("Read: %v", err)
	}
	if string(buf[:n]) != "hello 9front" {
		t.Errorf("read = %q, want %q", buf[:n], "hello 9front")
	}
}

func TestCreateWriteRead(t *testing.T) {
	fsys := memfs.New()
	c := startTestServer(t, fsys)

	// Create the file (the go9p test client ignores RCreate.Iounit, so the
	// create'd fid is no good for writing; close it and reopen for I/O).
	cf, err := c.Create("/out.txt", 0644)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	cf.Close()

	if _, err := c.Stat("/out.txt"); err != nil {
		t.Fatalf("Stat created file: %v", err)
	}

	f, err := c.Open("/out.txt", proto.Ordwr)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := f.Write([]byte("written over 9p")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	// Read back through the same open fid. (The go9p client's clunk is async,
	// so reading after Close would race the flush; same-fid ReadAt avoids that.)
	rb := make([]byte, 64)
	n, err := f.ReadAt(rb, 0)
	if err != nil && err != io.EOF {
		t.Fatalf("ReadAt: %v", err)
	}
	if string(rb[:n]) != "written over 9p" {
		t.Errorf("same-fid read = %q, want %q", rb[:n], "written over 9p")
	}
	f.Close()

	// Give the async clunk a moment to flush, then verify via the namespace
	// and a fresh 9P open that the write persisted to the backing store.
	time.Sleep(50 * time.Millisecond)
	if got, err := fs.ReadFile(fsys, "out.txt"); err != nil || string(got) != "written over 9p" {
		t.Errorf("namespace read = %q err=%v, want %q", got, err, "written over 9p")
	}
	f2, err := c.Open("/out.txt", proto.Oread)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer f2.Close()
	buf := make([]byte, 64)
	n, _ = f2.Read(buf)
	if string(buf[:n]) != "written over 9p" {
		t.Errorf("9p read = %q", buf[:n])
	}
}

func TestMkdirViaCreate(t *testing.T) {
	fsys := memfs.New()
	c := startTestServer(t, fsys)

	if _, err := c.Create("/d1", fs.ModeDir|0755); err != nil {
		t.Fatalf("Create dir: %v", err)
	}
	st, err := c.Stat("/d1")
	if err != nil {
		t.Fatalf("Stat /d1: %v", err)
	}
	if st.Mode&proto.DMDIR == 0 {
		t.Errorf("/d1 not a directory: mode=%o", st.Mode)
	}
}
