package p9p

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/knusbaum/go9p/proto"
	"tractor.dev/wanix/fs"
	"tractor.dev/wanix/fs/memfs"
)

func TestDirectoryReadRecordBoundaries(t *testing.T) {
	m := memfs.New()
	for _, name := range []string{"a", "a-much-longer-name", "z"} {
		if err := fs.WriteFile(m, name, []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	s := &srv{Fsys: m}
	listing, err := s.dirListing(".")
	if err != nil {
		t.Fatal(err)
	}
	first := int(binary.LittleEndian.Uint16(listing)) + 2
	for _, limit := range []uint32{uint32(first), uint32(first + 1), 128} {
		c := &conn{msize: limit + 11, fids: map[uint32]*fidState{1: {opened: true, isDir: true, dirBuf: listing}}}
		// A too-small count must fail, not masquerade as EOF.
		if _, ok := s.read(c, &proto.TRead{Fid: 1, Count: 1}).(*proto.RError); !ok {
			t.Fatal("short buffer accepted")
		}
		var data []byte
		for i := 0; i < 10; i++ {
			resp := s.read(c, &proto.TRead{Fid: 1, Offset: uint64(len(data)), Count: 4096})
			if e, ok := resp.(*proto.RError); ok {
				// The next entry may exceed this particular negotiated limit.
				if limit < 100 {
					break
				}
				t.Fatal(e.Ename)
			}
			r := resp.(*proto.RRead)
			if len(r.Data) == 0 {
				break
			}
			for b := r.Data; len(b) > 0; {
				if len(b) < 2 {
					t.Fatal("split record header")
				}
				n := int(binary.LittleEndian.Uint16(b)) + 2
				if n > len(b) {
					t.Fatal("split record")
				}
				b = b[n:]
			}
			data = append(data, r.Data...)
		}
		if limit == 128 && !bytes.Equal(data, listing) {
			t.Fatal("directory listing lost entries")
		}
		if _, ok := s.read(c, &proto.TRead{Fid: 1, Offset: 1, Count: 128}).(*proto.RError); !ok {
			t.Fatal("invalid offset accepted")
		}
	}
}

func TestNativeQIDIdentityAndVersions(t *testing.T) {
	m := memfs.New()
	if err := fs.WriteFile(m, "old", []byte("abc"), 0644); err != nil {
		t.Fatal(err)
	}
	qid := func(p string) proto.Qid {
		t.Helper()
		info, err := fs.Stat(m, p)
		if err != nil {
			t.Fatal(err)
		}
		return qidFor(p, info)
	}
	before := qid("old")
	f, err := m.Open("old")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for i := 0; i < 2; i++ {
		if _, err := fs.WriteAt(f, []byte("A"), 0); err != nil {
			t.Fatal(err)
		}
		after := qid("old")
		if after.Uid != before.Uid || after.Vers == before.Vers {
			t.Fatalf("write QID: %v -> %v", before, after)
		}
		before = after
	}
	if err := fs.Rename(m, "old", "new"); err != nil {
		t.Fatal(err)
	}
	if after := qid("new"); after.Uid != before.Uid {
		t.Fatal("rename changed identity")
	}
	if err := fs.Truncate(m, "new", 0); err != nil {
		t.Fatal(err)
	}
	if after := qid("new"); after.Vers == before.Vers {
		t.Fatal("truncate retained version")
	}
	if err := fs.Remove(m, "new"); err != nil {
		t.Fatal(err)
	}
	if err := fs.WriteFile(m, "new", []byte("abc"), 0644); err != nil {
		t.Fatal(err)
	}
	if after := qid("new"); after.Uid == before.Uid {
		t.Fatal("recreate reused identity")
	}
}
