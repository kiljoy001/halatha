package p9p

import (
	"io"
	"net"
	"strings"
	"testing"

	"tractor.dev/wanix/fs"
	"tractor.dev/wanix/fs/memfs"
)

func TestClientMountListsAndReads(t *testing.T) {
	backing := memfs.New()
	for _, name := range []string{"hello", "sub/second"} {
		if err := fs.MkdirAll(backing, "sub", 0755); err != nil {
			t.Fatal(err)
		}
		if err := fs.WriteFile(backing, name, []byte(strings.Repeat(name, 4000)), 0644); err != nil {
			t.Fatal(err)
		}
	}
	a, b := net.Pipe()
	t.Cleanup(func() { a.Close(); b.Close() })
	go Serve(backing, a, a)
	imported, err := ClientFS(b, "none", "")
	if err != nil {
		t.Fatal(err)
	}
	entries, err := fs.ReadDir(imported, ".")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].Name() != "hello" || entries[1].Name() != "sub" {
		t.Fatalf("entries: %v", entries)
	}
	data, err := fs.ReadFile(imported, "hello")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != strings.Repeat("hello", 4000) {
		t.Fatal("remote content mismatch")
	}
	f, err := imported.Open("sub/second")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	buf := make([]byte, 16000)
	n, err := f.(io.ReaderAt).ReadAt(buf, 3)
	if err != nil || n != len(buf) || string(buf) != strings.Repeat("sub/second", 4000)[3:3+len(buf)] {
		t.Fatalf("ReadAt: %d %v", n, err)
	}
	_, err = fs.Write(f, []byte("must fail"))
	if err == nil {
		t.Fatal("read-only import allowed write")
	}
	if _, err := imported.Open("missing"); err == nil {
		t.Fatal("missing file opened")
	}
}
