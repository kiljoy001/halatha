package p9p

import (
	"bytes"
	"io"
	"math/rand"
	"net"
	"os"
	"strings"
	"testing"
	"testing/fstest"
	"testing/quick"
	"time"

	"github.com/knusbaum/go9p/proto"
	"tractor.dev/wanix/fs"
	"tractor.dev/wanix/fs/memfs"
)

// mount serves backing over 9P2000 and returns the client's view of it.
func mount(t *testing.T, backing fs.FS) fs.FS {
	t.Helper()
	a, b := net.Pipe()
	t.Cleanup(func() { a.Close(); b.Close() })
	go Serve(backing, a, a)
	imported, err := ClientFS(b, "none", "")
	if err != nil {
		t.Fatal(err)
	}
	return imported
}

func readBacking(t *testing.T, backing fs.FS, name string) string {
	t.Helper()
	b, err := fs.ReadFile(backing, name)
	if err != nil {
		t.Fatalf("backing %s: %v", name, err)
	}
	return string(b)
}

// Writes through the import land in the served filesystem: create, write,
// then O_TRUNC replaces the content instead of overwriting its prefix.
func TestClientCreateWriteTruncate(t *testing.T) {
	backing := memfs.New()
	imported := mount(t, backing)

	f, err := fs.Create(imported, "new.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fs.Write(f, []byte("hello, 9P2000")); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if got := readBacking(t, backing, "new.txt"); got != "hello, 9P2000" {
		t.Fatalf("after create: %q", got)
	}

	f, err = fs.OpenFile(imported, "new.txt", os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fs.Write(f, []byte("bye")); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if got := readBacking(t, backing, "new.txt"); got != "bye" {
		t.Fatalf("after O_TRUNC: %q", got)
	}
}

// fs.WriteFile goes through OpenFile with O_CREATE; writes larger than the
// negotiated iounit must be split, not truncated or rejected.
func TestClientWriteFileLargerThanIOUnit(t *testing.T) {
	backing := memfs.New()
	imported := mount(t, backing)
	data := strings.Repeat("0123456789abcdef", 16*1024) // 256 KiB
	if err := fs.WriteFile(imported, "big", []byte(data), 0644); err != nil {
		t.Fatal(err)
	}
	if got := readBacking(t, backing, "big"); got != data {
		t.Fatalf("big write: got %d bytes, want %d", len(got), len(data))
	}
}

func TestClientMkdirCreateRemove(t *testing.T) {
	backing := memfs.New()
	imported := mount(t, backing)
	if err := fs.Mkdir(imported, "d", 0755); err != nil {
		t.Fatal(err)
	}
	if info, err := fs.Stat(backing, "d"); err != nil || !info.IsDir() {
		t.Fatalf("mkdir: %v %v", info, err)
	}
	if err := fs.WriteFile(imported, "d/f", []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := fs.Remove(imported, "d/f"); err != nil {
		t.Fatal(err)
	}
	if err := fs.Remove(imported, "d"); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.Stat(backing, "d"); err == nil {
		t.Fatal("directory still exists after remove")
	}
}

// A 9P2000 rename is a wstat of the name. The other fields carry the
// "don't touch" value, so the file keeps its mode and its content.
func TestClientRenameKeepsModeAndContent(t *testing.T) {
	backing := memfs.New()
	if err := fs.WriteFile(backing, "a", []byte("content"), 0644); err != nil {
		t.Fatal(err)
	}
	// memfs.WriteFile ignores perm, so set the distinctive mode explicitly.
	if err := fs.Chmod(backing, "a", 0640); err != nil {
		t.Fatal(err)
	}
	imported := mount(t, backing)
	if err := fs.Rename(imported, "a", "b"); err != nil {
		t.Fatal(err)
	}
	info, err := fs.Stat(backing, "b")
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0640 {
		t.Errorf("rename changed mode to %v", info.Mode().Perm())
	}
	if got := readBacking(t, backing, "b"); got != "content" {
		t.Errorf("rename changed content to %q", got)
	}
}

// 9P2000 renames within a directory only; a move elsewhere must fail
// rather than silently land somewhere else.
func TestClientRenameAcrossDirectoriesFails(t *testing.T) {
	backing := memfs.New()
	fs.MkdirAll(backing, "x", 0755)
	fs.WriteFile(backing, "a", []byte("1"), 0644)
	imported := mount(t, backing)
	if err := fs.Rename(imported, "a", "x/a"); err == nil {
		t.Fatal("cross-directory rename succeeded")
	}
	if got := readBacking(t, backing, "a"); got != "1" {
		t.Fatalf("source changed: %q", got)
	}
}

func TestClientChmodKeepsContent(t *testing.T) {
	backing := memfs.New()
	fs.WriteFile(backing, "f", []byte("keep me"), 0644)
	imported := mount(t, backing)
	if err := fs.Chmod(imported, "f", 0600); err != nil {
		t.Fatal(err)
	}
	info, _ := fs.Stat(backing, "f")
	if info.Mode().Perm() != 0600 {
		t.Errorf("mode %v, want 0600", info.Mode().Perm())
	}
	if got := readBacking(t, backing, "f"); got != "keep me" {
		t.Errorf("chmod changed content to %q", got)
	}
}

// Length zero is a real request, not "don't touch": it truncates.
func TestClientTruncate(t *testing.T) {
	backing := memfs.New()
	fs.WriteFile(backing, "f", []byte("123456789"), 0644)
	imported := mount(t, backing)
	if err := fs.Truncate(imported, "f", 3); err != nil {
		t.Fatal(err)
	}
	if got := readBacking(t, backing, "f"); got != "123" {
		t.Fatalf("truncate 3: %q", got)
	}
	if err := fs.Truncate(imported, "f", 0); err != nil {
		t.Fatal(err)
	}
	if got := readBacking(t, backing, "f"); got != "" {
		t.Fatalf("truncate 0: %q", got)
	}
}

func TestClientChtimesSetsMtime(t *testing.T) {
	backing := memfs.New()
	fs.WriteFile(backing, "f", []byte("x"), 0644)
	imported := mount(t, backing)
	when := time.Unix(1700000000, 0)
	if err := fs.Chtimes(imported, "f", when, when); err != nil {
		t.Fatal(err)
	}
	info, _ := fs.Stat(backing, "f")
	if !info.ModTime().Equal(when) {
		t.Errorf("mtime %v, want %v", info.ModTime(), when)
	}
}

// Plan 9 names owners, it does not number them, so a numeric chown has no
// 9P2000 meaning and must say so instead of pretending.
func TestClientChownUnsupported(t *testing.T) {
	backing := memfs.New()
	fs.WriteFile(backing, "f", []byte("x"), 0644)
	imported := mount(t, backing)
	if cfs, ok := imported.(fs.ChownFS); ok {
		if err := cfs.Chown("f", 1, 1); err == nil {
			t.Error("numeric chown reported success")
		}
	}
}

// The server side of the same rule, as a Plan 9 kernel sends it: a wstat
// whose fields are all "don't touch" changes nothing.
func TestServerWstatDontTouchChangesNothing(t *testing.T) {
	backing := memfs.New()
	fs.WriteFile(backing, "f", []byte("unchanged"), 0644)
	fs.Chmod(backing, "f", 0640) // memfs.WriteFile ignores perm
	a, b := net.Pipe()
	t.Cleanup(func() { a.Close(); b.Close() })
	go Serve(backing, a, a)
	rpc := func(call proto.FCall) proto.FCall {
		t.Helper()
		if _, err := b.Write(call.Compose()); err != nil {
			t.Fatal(err)
		}
		r, err := proto.ParseCall(b)
		if err != nil {
			t.Fatal(err)
		}
		if e, ok := r.(*proto.RError); ok {
			t.Fatalf("%T: %s", call, e.Ename)
		}
		return r
	}
	rpc(&proto.TRVersion{Header: proto.Header{Type: proto.Tversion, Tag: 0xffff}, Msize: 8192, Version: "9P2000"})
	rpc(&proto.TAttach{Header: proto.Header{Type: proto.Tattach, Tag: 1}, Fid: 1, Afid: ^uint32(0), Uname: "none"})
	rpc(&proto.TWalk{Header: proto.Header{Type: proto.Twalk, Tag: 2}, Fid: 1, Newfid: 2, Nwname: 1, Wname: []string{"f"}})
	rpc(&proto.TWstat{Header: proto.Header{Type: proto.Twstat, Tag: 3}, Fid: 2, Stat: dontTouch()})

	info, _ := fs.Stat(backing, "f")
	if info.Mode().Perm() != 0640 {
		t.Errorf("don't-touch wstat changed mode to %v", info.Mode().Perm())
	}
	if got := readBacking(t, backing, "f"); got != "unchanged" {
		t.Errorf("don't-touch wstat changed content to %q", got)
	}
}

// A rename the backing store cannot perform is an error the client sees,
// never a silent success that leaves the old name in place.
func TestServerReportsFailedRename(t *testing.T) {
	imported := mount(t, fstest.MapFS{"f": {Data: []byte("x"), Mode: 0644}})
	if err := fs.Rename(imported, "f", "g"); err == nil {
		t.Fatal("rename on a store that cannot rename reported success")
	}
}

// Mode is metadata a read-only store may be unable to keep; like a Plan 9
// file server with no permission bits, that is not a failed request.
func TestServerIgnoresUnsupportedChmod(t *testing.T) {
	imported := mount(t, fstest.MapFS{"f": {Data: []byte("x"), Mode: 0644}})
	if err := fs.Chmod(imported, "f", 0600); err != nil {
		t.Fatalf("chmod on a store without modes failed: %v", err)
	}
}

// A wstat name is a single path element. Accepting "x/a" would let a rename
// move the file into another directory, which 9P2000 does not allow.
func TestServerRejectsSlashInWstatName(t *testing.T) {
	backing := memfs.New()
	fs.MkdirAll(backing, "x", 0755)
	fs.WriteFile(backing, "f", []byte("stay"), 0644)
	a, b := net.Pipe()
	t.Cleanup(func() { a.Close(); b.Close() })
	go Serve(backing, a, a)
	send := func(call proto.FCall) proto.FCall {
		t.Helper()
		if _, err := b.Write(call.Compose()); err != nil {
			t.Fatal(err)
		}
		r, err := proto.ParseCall(b)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	send(&proto.TRVersion{Header: proto.Header{Type: proto.Tversion, Tag: 0xffff}, Msize: 8192, Version: "9P2000"})
	send(&proto.TAttach{Header: proto.Header{Type: proto.Tattach, Tag: 1}, Fid: 1, Afid: ^uint32(0), Uname: "none"})
	send(&proto.TWalk{Header: proto.Header{Type: proto.Twalk, Tag: 2}, Fid: 1, Newfid: 2, Nwname: 1, Wname: []string{"f"}})
	st := dontTouch()
	st.Name = "x/a"
	if _, ok := send(&proto.TWstat{Header: proto.Header{Type: proto.Twstat, Tag: 3}, Fid: 2, Stat: st}).(*proto.RError); !ok {
		t.Error("wstat with a slash in the name was accepted")
	}
	if _, err := fs.Stat(backing, "x/a"); err == nil {
		t.Error("file was moved into another directory")
	}
	if got := readBacking(t, backing, "f"); got != "stay" {
		t.Errorf("original changed: %q", got)
	}
}

// Property: any sequence of writes leaves the served file equal to the same
// writes applied to a byte slice. Offsets stay within the current length:
// fskit files, like memfs here, refuse a write that would leave a gap past
// the end, so sparse writes are a property of the backing store, not of 9P.
func TestClientWriteAtMatchesModel(t *testing.T) {
	check := func(seed int64) bool {
		rng := rand.New(rand.NewSource(seed))
		backing := memfs.New()
		imported := mount(t, backing)
		f, err := fs.OpenFile(imported, "m", os.O_RDWR|os.O_CREATE, 0644)
		if err != nil {
			return false
		}
		defer f.Close()
		w, ok := f.(io.WriterAt)
		if !ok {
			return false
		}
		var model []byte
		for i := 0; i < 1+rng.Intn(12); i++ {
			off := rng.Intn(len(model) + 1)
			data := make([]byte, rng.Intn(20000))
			rng.Read(data)
			if _, err := w.WriteAt(data, int64(off)); err != nil {
				return false
			}
			if end := off + len(data); end > len(model) {
				model = append(model, make([]byte, end-len(model))...)
			}
			copy(model[off:], data)
		}
		got, err := fs.ReadFile(backing, "m")
		return err == nil && bytes.Equal(got, model)
	}
	if err := quick.Check(check, &quick.Config{MaxCount: 25}); err != nil {
		t.Error(err)
	}
}

// Whatever a wstat carries, the server answers it without panicking, and a
// field sent as "don't touch" is left exactly as it was.
func FuzzServerWstat(f *testing.F) {
	f.Add(uint32(0xffffffff), uint64(0xffffffffffffffff), uint32(0xffffffff), "")
	f.Add(uint32(0600), uint64(0), uint32(1700000000), "renamed")
	f.Fuzz(func(t *testing.T, mode uint32, length uint64, mtime uint32, name string) {
		// Growing a file by wstat is legal 9P2000; keep the fuzzer's
		// lengths small so it tests the protocol, not the host's memory.
		if length != ^uint64(0) {
			length %= 1 << 16
		}
		backing := memfs.New()
		fs.WriteFile(backing, "f", []byte("x"), 0644)
		a, b := net.Pipe()
		defer a.Close()
		defer b.Close()
		go Serve(backing, a, a)
		st := dontTouch()
		st.Mode, st.Length, st.Mtime, st.Name = mode, length, mtime, name
		for _, call := range []proto.FCall{
			&proto.TRVersion{Header: proto.Header{Type: proto.Tversion, Tag: 0xffff}, Msize: 8192, Version: "9P2000"},
			&proto.TAttach{Header: proto.Header{Type: proto.Tattach, Tag: 1}, Fid: 1, Afid: ^uint32(0), Uname: "none"},
			&proto.TWalk{Header: proto.Header{Type: proto.Twalk, Tag: 2}, Fid: 1, Newfid: 2, Nwname: 1, Wname: []string{"f"}},
			&proto.TWstat{Header: proto.Header{Type: proto.Twstat, Tag: 3}, Fid: 2, Stat: st},
		} {
			if _, err := b.Write(call.Compose()); err != nil {
				t.Fatal(err)
			}
			if _, err := proto.ParseCall(b); err != nil {
				t.Fatal(err)
			}
		}
		if name != "" {
			return // renamed or refused; the don't-touch checks below need "f"
		}
		info, err := fs.Stat(backing, "f")
		if err != nil {
			t.Fatalf("file vanished: %v", err)
		}
		if mode == ^uint32(0) && info.Mode().Perm() != 0644 {
			t.Fatalf("don't-touch mode changed it to %v", info.Mode().Perm())
		}
		if length == ^uint64(0) && readBacking(t, backing, "f") != "x" {
			t.Fatal("don't-touch length changed the content")
		}
	})
}
