package p9p

import (
	"strings"
	"testing"

	"github.com/knusbaum/go9p/proto"

	"tractor.dev/wanix/fs"
	"tractor.dev/wanix/fs/fskit"
	"tractor.dev/wanix/fs/memfs"
)

// TestVersionClampsMsize pins msize negotiation. A 9P server must never emit
// a message larger than the negotiated msize, so a client asking for 0 or
// for more than MaxMsgLen has to be clamped, and the Rversion must report
// the clamped value back.
func TestVersionClampsMsize(t *testing.T) {
	cases := []struct {
		name string
		ask  uint32
		want uint32
	}{
		{"zero clamps to max", 0, proto.MaxMsgLen},
		{"over max clamps to max", proto.MaxMsgLen + 1, proto.MaxMsgLen},
		{"way over max clamps", 0xFFFFFFFF, proto.MaxMsgLen},
		{"exact max is kept", proto.MaxMsgLen, proto.MaxMsgLen},
		{"small value is kept", 8192, 8192},
		{"tiny value is kept", 12, 12},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &srv{}
			c := &conn{}
			resp := s.version(c, &proto.TRVersion{
				Header:  proto.Header{Type: proto.Tversion, Tag: 1},
				Msize:   tc.ask,
				Version: "9P2000",
			})
			rv, ok := resp.(*proto.TRVersion)
			if !ok {
				t.Fatalf("response type %T, want *proto.TRVersion", resp)
			}
			if rv.Msize != tc.want {
				t.Errorf("Rversion msize = %d, want %d", rv.Msize, tc.want)
			}
			if c.msize != tc.want {
				t.Errorf("conn msize = %d, want %d", c.msize, tc.want)
			}
			if rv.Version != "9P2000" {
				t.Errorf("version = %q", rv.Version)
			}
		})
	}
}

// TestVersionRejectsUnsupported pins protocol version gating.
func TestVersionRejectsUnsupported(t *testing.T) {
	for _, v := range []string{"9P2000.u", "9P2000.L", "unknown", ""} {
		s := &srv{}
		c := &conn{}
		resp := s.version(c, &proto.TRVersion{
			Header:  proto.Header{Type: proto.Tversion, Tag: 1},
			Msize:   8192,
			Version: v,
		})
		if _, isErr := resp.(*proto.RError); !isErr {
			t.Errorf("version %q: got %T, want *proto.RError", v, resp)
		}
	}
}

// TestIOUnitNeverExceedsMsize is the core safety property: the iounit we
// advertise must always leave room for the Rread header, or a trusting
// client will request more data than its own buffer can hold.
func TestIOUnitNeverExceedsMsize(t *testing.T) {
	for _, msize := range []uint32{0, 1, 11, 12, 100, 8192, proto.IOUnit, proto.IOUnit + 100, proto.MaxMsgLen} {
		c := &conn{msize: msize}
		u := c.ioUnit()

		if msize > 11 && u > msize-11 {
			t.Errorf("msize %d: ioUnit %d exceeds msize-11 (%d)", msize, u, msize-11)
		}
		if u > proto.IOUnit {
			t.Errorf("msize %d: ioUnit %d exceeds proto.IOUnit %d", msize, u, proto.IOUnit)
		}
		// A degenerate msize must yield 0, never an underflowed huge number.
		if msize <= 11 && u != 0 {
			t.Errorf("msize %d: ioUnit = %d, want 0", msize, u)
		}
	}
}

// TestIOUnitNoUnderflow pins the boundary where msize-11 would wrap around
// on unsigned arithmetic.
func TestIOUnitNoUnderflow(t *testing.T) {
	for msize := uint32(0); msize <= 13; msize++ {
		c := &conn{msize: msize}
		u := c.ioUnit()
		if msize <= 11 {
			if u != 0 {
				t.Errorf("msize %d: ioUnit = %d, want 0", msize, u)
			}
			continue
		}
		if u != msize-11 {
			t.Errorf("msize %d: ioUnit = %d, want %d", msize, u, msize-11)
		}
	}
}

// TestReadMaxNoUnderflow pins the same boundary for readMax, which bounds
// how much data a single Rread may carry.
func TestReadMaxNoUnderflow(t *testing.T) {
	for msize := uint32(0); msize <= 13; msize++ {
		c := &conn{msize: msize}
		m := c.readMax()
		if msize <= 11 {
			if m != proto.IOUnit {
				t.Errorf("msize %d: readMax = %d, want proto.IOUnit", msize, m)
			}
			continue
		}
		if m != msize-11 {
			t.Errorf("msize %d: readMax = %d, want %d", msize, m, msize-11)
		}
	}
}

// FuzzReadBounds drives the Tread path with arbitrary offsets and counts
// against a directory buffer. The guest controls both fields, so no
// combination may panic or produce a reply larger than the negotiated msize.
func FuzzReadBounds(f *testing.F) {
	f.Add(uint32(8192), uint64(0), uint32(100), 50)
	f.Add(uint32(12), uint64(0), uint32(0xFFFFFFFF), 10)
	f.Add(uint32(0), uint64(0xFFFFFFFFFFFFFFFF), uint32(0xFFFFFFFF), 0)
	f.Add(uint32(proto.MaxMsgLen), uint64(1), uint32(1), 1)

	f.Fuzz(func(t *testing.T, msize uint32, offset uint64, count uint32, bufLen int) {
		if bufLen < 0 || bufLen > 1<<16 {
			return
		}
		c := &conn{msize: msize, fids: map[uint32]*fidState{
			1: {opened: true, isDir: true, dirBuf: make([]byte, bufLen)},
		}}

		s := &srv{}
		resp := s.read(c, &proto.TRead{
			Header: proto.Header{Type: proto.Tread, Tag: 1},
			Fid:    1,
			Offset: offset,
			Count:  count,
		})

		rr, ok := resp.(*proto.RRead)
		if !ok {
			return // an RError is a legitimate outcome
		}
		// The reply must never exceed what this conn may carry.
		if rr.Count > c.readMax() {
			t.Fatalf("Rread count %d exceeds readMax %d (msize=%d)", rr.Count, c.readMax(), msize)
		}
		if int(rr.Count) != len(rr.Data) {
			t.Fatalf("Count %d disagrees with len(Data) %d", rr.Count, len(rr.Data))
		}
		// Reads at or past EOF must report zero, never wrap into data.
		if offset >= uint64(bufLen) && rr.Count != 0 {
			t.Fatalf("offset %d past buffer %d returned %d bytes", offset, bufLen, rr.Count)
		}
	})
}

// TestJoinPath pins namespace path construction. The client supplies walk
// names, so this is the seam where a malformed component could escape the
// namespace or produce a path wanix cannot resolve.
func TestJoinPath(t *testing.T) {
	cases := []struct{ base, name, want string }{
		{".", "foo", "foo"},
		{"", "foo", "foo"},
		{"foo", ".", "foo"},
		{"foo", "", "foo"},
		{".", ".", "."},
		{"", "", ""},
		{"foo", "bar", "foo/bar"},
		{"foo/bar", "baz", "foo/bar/baz"},
		{".", ".", "."},
		// path.Join cleans traversal, keeping results namespace-relative.
		{"foo", "..", "."},
		{"foo/bar", "..", "foo"},
		{"foo", "./bar", "foo/bar"},
	}
	for _, tc := range cases {
		if got := joinPath(tc.base, tc.name); got != tc.want {
			t.Errorf("joinPath(%q, %q) = %q, want %q", tc.base, tc.name, got, tc.want)
		}
	}
}

// TestJoinPathNeverAbsolute is the security property: wanix namespace paths
// are relative, and fs.ValidPath rejects a leading slash. No combination of
// base and component may produce an absolute path.
func TestJoinPathNeverAbsolute(t *testing.T) {
	bases := []string{".", "", "foo", "foo/bar"}
	names := []string{".", "", "x", "..", "../..", "/etc/passwd", "//x", "a/../../b"}
	for _, b := range bases {
		for _, n := range names {
			got := joinPath(b, n)
			if strings.HasPrefix(got, "/") && !strings.HasPrefix(b, "/") {
				t.Errorf("joinPath(%q, %q) = %q escaped to an absolute path", b, n, got)
			}
		}
	}
}

// TestStatForRootNaming pins the root rename to "/" that 9P clients expect,
// without renaming ordinary entries.
func TestStatForRootNaming(t *testing.T) {
	root := statFor(".", fskit.Entry("anything", fs.ModeDir|0755))
	if root.Name != "/" {
		t.Errorf("root stat name = %q, want /", root.Name)
	}
	if root.Mode&proto.DMDIR == 0 {
		t.Error("root stat missing DMDIR")
	}

	child := statFor("foo/bar", fskit.Entry("bar", 0644))
	if child.Name != "bar" {
		t.Errorf("child stat name = %q, want bar", child.Name)
	}
	if child.Mode&proto.DMDIR != 0 {
		t.Error("regular file stat has DMDIR set")
	}
}

// TestQidForDistinguishesPaths pins qid synthesis: distinct paths must get
// distinct ids, and directories must carry the directory qtype bit.
func TestQidForDistinguishesPaths(t *testing.T) {
	node := fskit.Entry("foo", 0644)
	a := qidFor("foo", node)
	b := qidFor("bar", fskit.Entry("bar", 0644))
	if a.Uid == b.Uid {
		t.Error("distinct paths produced the same qid")
	}
	if again := qidFor("renamed", node); again.Uid != a.Uid {
		t.Error("qid for the same path is not stable")
	}

	dir := qidFor("d", fskit.Entry("d", fs.ModeDir|0755))
	if dir.Qtype&0x80 == 0 {
		t.Error("directory qid missing the 0x80 qtype bit")
	}
	if a.Qtype&0x80 != 0 {
		t.Error("file qid has the directory bit set")
	}
}

// TestOpenFileHonorsWriteMode is the regression test for a silent data-loss
// bug: openFile used a read-only fs.OpenContext and discarded the 9P open
// mode. Twrite then reported success (count = len(data)) while the bytes
// went nowhere, so `echo x > /n/host/clip/text` in the guest appeared to
// hang or succeed and wrote nothing. Otrunc made it worse by emptying the
// file first.
func TestOpenFileHonorsWriteMode(t *testing.T) {
	m := memfs.New()
	if err := fs.WriteFile(m, "clip.txt", []byte("initial\n"), 0666); err != nil {
		t.Fatal(err)
	}
	s := &srv{Fsys: m}
	c := &conn{msize: 8192, fids: map[uint32]*fidState{1: {path: "clip.txt"}}}

	resp := s.open(c, &proto.TOpen{
		Header: proto.Header{Type: proto.Topen, Tag: 1},
		Fid:    1,
		Mode:   proto.Owrite | proto.Otrunc,
	})
	if e, bad := resp.(*proto.RError); bad {
		t.Fatalf("open for write failed: %s", e.Ename)
	}

	const payload = "hello from plan 9\n"
	w := s.write(c, &proto.TWrite{
		Header: proto.Header{Type: proto.Twrite, Tag: 2},
		Fid:    1,
		Offset: 0,
		Data:   []byte(payload),
	})
	if e, bad := w.(*proto.RError); bad {
		t.Fatalf("write rejected: %s", e.Ename)
	}
	if got := w.(*proto.RWrite).Count; int(got) != len(payload) {
		t.Errorf("Rwrite count = %d, want %d", got, len(payload))
	}

	// memfs commits a written handle on Close, which 9P does via Tclunk.
	// A real guest always clunks after `echo >`, so clunk here too.
	if e, bad := s.clunk(c, &proto.TClunk{
		Header: proto.Header{Type: proto.Tclunk, Tag: 3}, Fid: 1,
	}).(*proto.RError); bad {
		t.Fatalf("clunk failed: %s", e.Ename)
	}

	// The decisive assertion: the bytes must actually be in the file. The
	// old code passed the count check above and failed this one.
	got, err := fs.ReadFile(m, "clip.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != payload {
		t.Errorf("file contents = %q, want %q (write was silently discarded)", got, payload)
	}
}

// TestOpenFileReadModeStillWorks guards the other direction, so the fix
// cannot be satisfied by opening everything read-write.
func TestOpenFileReadModeStillWorks(t *testing.T) {
	m := memfs.New()
	if err := fs.WriteFile(m, "r.txt", []byte("readable\n"), 0444); err != nil {
		t.Fatal(err)
	}
	s := &srv{Fsys: m}
	c := &conn{msize: 8192, fids: map[uint32]*fidState{1: {path: "r.txt"}}}

	resp := s.open(c, &proto.TOpen{
		Header: proto.Header{Type: proto.Topen, Tag: 1},
		Fid:    1,
		Mode:   proto.Oread,
	})
	if e, bad := resp.(*proto.RError); bad {
		t.Fatalf("open for read failed: %s", e.Ename)
	}
	rd := s.read(c, &proto.TRead{
		Header: proto.Header{Type: proto.Tread, Tag: 2},
		Fid:    1,
		Offset: 0,
		Count:  64,
	})
	if e, bad := rd.(*proto.RError); bad {
		t.Fatalf("read failed: %s", e.Ename)
	}
	if got := string(rd.(*proto.RRead).Data); got != "readable\n" {
		t.Errorf("read got %q", got)
	}
}

// TestOpenFileRdwrRoundTrip pins Ordwr, which the clipboard sync daemon uses.
func TestOpenFileRdwrRoundTrip(t *testing.T) {
	m := memfs.New()
	if err := fs.WriteFile(m, "rw.txt", []byte("before\n"), 0666); err != nil {
		t.Fatal(err)
	}
	s := &srv{Fsys: m}
	c := &conn{msize: 8192, fids: map[uint32]*fidState{1: {path: "rw.txt"}}}

	if e, bad := s.open(c, &proto.TOpen{
		Header: proto.Header{Type: proto.Topen, Tag: 1}, Fid: 1, Mode: proto.Ordwr,
	}).(*proto.RError); bad {
		t.Fatalf("open rdwr failed: %s", e.Ename)
	}
	if e, bad := s.write(c, &proto.TWrite{
		Header: proto.Header{Type: proto.Twrite, Tag: 2}, Fid: 1, Offset: 0, Data: []byte("after!\n"),
	}).(*proto.RError); bad {
		t.Fatalf("rdwr write rejected: %s", e.Ename)
	}
	if e, bad := s.clunk(c, &proto.TClunk{
		Header: proto.Header{Type: proto.Tclunk, Tag: 3}, Fid: 1,
	}).(*proto.RError); bad {
		t.Fatalf("clunk failed: %s", e.Ename)
	}
	got, err := fs.ReadFile(m, "rw.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "after!\n" {
		t.Errorf("rdwr write did not land: %q", got)
	}
}

// TestOtruncActuallyTruncates is the regression test for silent clipboard
// corruption. openFile truncated AFTER opening, but an open handle has
// already snapshotted the file, so the stale tail was written back on close:
// `echo hello from plan 9 > /n/host/clip/text` over the default
// "browser clipboard bridge ready\n" produced
// "hello from plan 9\nbridge ready\n". The guest reported success and the
// clipboard silently carried garbage.
func TestOtruncActuallyTruncates(t *testing.T) {
	m := memfs.New()
	if err := fs.MkdirAll(m, "clip", 0777); err != nil {
		t.Fatal(err)
	}
	const stale = "browser clipboard bridge ready\n"
	if err := fs.WriteFile(m, "clip/text", []byte(stale), 0666); err != nil {
		t.Fatal(err)
	}

	s := &srv{Fsys: m}
	c := &conn{msize: 8192, fids: map[uint32]*fidState{1: {path: "."}}}

	// The exact sequence rc performs for `echo ... > /n/host/clip/text`.
	if e, bad := s.walk(c, &proto.TWalk{
		Header: proto.Header{Type: proto.Twalk, Tag: 1},
		Fid:    1, Newfid: 2, Wname: []string{"clip", "text"},
	}).(*proto.RError); bad {
		t.Fatalf("walk failed: %s", e.Ename)
	}
	if e, bad := s.open(c, &proto.TOpen{
		Header: proto.Header{Type: proto.Topen, Tag: 2},
		Fid:    2, Mode: proto.Owrite | proto.Otrunc,
	}).(*proto.RError); bad {
		t.Fatalf("open failed: %s", e.Ename)
	}

	const payload = "hello from plan 9\n"
	if e, bad := s.write(c, &proto.TWrite{
		Header: proto.Header{Type: proto.Twrite, Tag: 3},
		Fid:    2, Offset: 0, Data: []byte(payload),
	}).(*proto.RError); bad {
		t.Fatalf("write failed: %s", e.Ename)
	}
	s.clunk(c, &proto.TClunk{Header: proto.Header{Type: proto.Tclunk, Tag: 4}, Fid: 2})

	got, err := fs.ReadFile(m, "clip/text")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != payload {
		t.Errorf("Otrunc did not truncate:\n got  %q\n want %q", got, payload)
	}
}

// TestOtruncShorterThenLonger guards the general case, not just the one
// length pair that happened to expose the bug.
func TestOtruncShorterThenLonger(t *testing.T) {
	cases := []struct{ initial, write string }{
		{"0123456789", "hi\n"},
		{"short", "a much longer replacement string\n"},
		{"", "from empty\n"},
		{"exactly-same-len", "ExAcTlY-sAmE-lEn"},
	}
	for _, tc := range cases {
		m := memfs.New()
		if err := fs.WriteFile(m, "f", []byte(tc.initial), 0666); err != nil {
			t.Fatal(err)
		}
		s := &srv{Fsys: m}
		c := &conn{msize: 8192, fids: map[uint32]*fidState{1: {path: "f"}}}
		s.open(c, &proto.TOpen{Header: proto.Header{Type: proto.Topen, Tag: 1},
			Fid: 1, Mode: proto.Owrite | proto.Otrunc})
		s.write(c, &proto.TWrite{Header: proto.Header{Type: proto.Twrite, Tag: 2},
			Fid: 1, Offset: 0, Data: []byte(tc.write)})
		s.clunk(c, &proto.TClunk{Header: proto.Header{Type: proto.Tclunk, Tag: 3}, Fid: 1})

		got, _ := fs.ReadFile(m, "f")
		if string(got) != tc.write {
			t.Errorf("initial %q + write %q = %q, want %q", tc.initial, tc.write, got, tc.write)
		}
	}
}
