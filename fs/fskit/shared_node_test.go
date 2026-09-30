package fskit

import (
	"io"
	"sync"
	"testing"
)

func TestDisjointHandleWritesSurviveEitherCloseOrder(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		n := Entry("abc", 0644, []byte("abc"))
		a, _ := n.Open(".")
		b, _ := n.Open(".")
		var wg sync.WaitGroup
		for i, f := range []io.WriterAt{a.(io.WriterAt), b.(io.WriterAt)} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if _, err := f.WriteAt([]byte{byte('A' + i)}, int64(i)); err != nil {
					t.Error(err)
				}
			}()
		}
		wg.Wait()
		if got := string(n.Data()); got != "ABc" {
			t.Fatalf("before close: %q", got)
		}
		if reverse {
			b.Close()
			a.Close()
		} else {
			a.Close()
			b.Close()
		}
		if got := string(n.Data()); got != "ABc" {
			t.Fatalf("after close: %q", got)
		}
	}
}

func TestWriteAtDoesNotMoveCursor(t *testing.T) {
	n := Entry("f", 0644, []byte("abc"))
	f, _ := n.Open(".")
	defer f.Close()
	if _, err := f.(io.WriterAt).WriteAt([]byte("B"), 1); err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 1)
	if _, err := f.Read(b); err != nil || string(b) != "a" {
		t.Fatalf("cursor moved: %q %v", b, err)
	}
}

func TestOpenHandlesObserveTruncate(t *testing.T) {
	n := Entry("f", 0644, []byte("long original"))
	f, _ := n.Open(".")
	defer f.Close()
	before := n.ModTime()
	_, version := n.FileIdentity()
	if err := n.Truncate(0); err != nil {
		t.Fatal(err)
	}
	_, after := n.FileIdentity()
	if after == version || !n.ModTime().After(before) {
		t.Fatal("truncate did not update metadata")
	}
	if _, err := f.(io.Writer).Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if got := string(n.Data()); got != "x" {
		t.Fatalf("stale data after truncate: %q", got)
	}
}

func TestNodeOwnsInitialBytesAndCloneIdentity(t *testing.T) {
	initial := []byte("abc")
	n := Entry("file", 0644, initial)
	f, _ := n.Open(".")
	defer f.Close()
	if _, err := f.(io.WriterAt).WriteAt([]byte("A"), 0); err != nil {
		t.Fatal(err)
	}
	if string(initial) != "abc" {
		t.Fatalf("write changed caller bytes: %q", initial)
	}
	clone := CloneNode(n)
	id, _ := n.FileIdentity()
	cloneID, _ := clone.FileIdentity()
	if id == cloneID {
		t.Fatal("independent copy reused identity")
	}
	SetData(clone, []byte("other"))
	if string(n.Data()) != "Abc" {
		t.Fatal("clone changed original")
	}
	wrapper := RawNode(n, "renamed")
	wrapperID, _ := wrapper.FileIdentity()
	if wrapperID != id {
		t.Fatal("metadata wrapper lost identity")
	}
}
