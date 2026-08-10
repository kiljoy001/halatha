// Package p9p serves a wanix filesystem (fs.FS) over the original 9P2000
// protocol (the Plan 9 dialect), as opposed to fs/p9kit which serves
// 9P2000.L. This is what a 9front guest speaks, so the wanix namespace can be
// mounted natively by 9front.
//
// It is a thin 9P2000 server over a path-based fid table: each fid maps to a
// namespace path (and an open fs.File once opened). It is transport-agnostic;
// callers feed it an io.Reader/io.Writer pair (e.g. a VM serial channel).
//
// Only github.com/knusbaum/go9p/proto is used (the wire codec), so this package
// builds under js/wasm — the full go9p server package pulls plan9/mux9p deps
// that don't build in the browser.
package p9p

import (
	"context"
	"errors"
	"hash/fnv"
	"io"
	"log"
	pathpkg "path"
	"sync"

	"github.com/knusbaum/go9p/proto"

	"tractor.dev/wanix/fs"
)

// Serve serves fsys over 9P2000 on the given reader/writer (e.g. a VM serial
// channel). It blocks until the connection closes or errors.
func Serve(fsys fs.FS, r io.Reader, w io.Writer) error {
	s := &srv{Fsys: fsys, w: w}
	c := &conn{fids: make(map[uint32]*fidState), msize: proto.MaxMsgLen}
	for {
		call, err := proto.ParseCall(r)
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		if resp := s.handle(c, call); resp != nil {
			if _, err := w.Write(resp.Compose()); err != nil {
				return err
			}
		}
	}
}

type srv struct {
	Fsys fs.FS
	w    io.Writer
}

// conn holds the per-connection fid table.
type conn struct {
	mu    sync.Mutex
	fids  map[uint32]*fidState
	msize uint32
}

type fidState struct {
	path   string  // wanix namespace path; "." is the root
	file   fs.File // non-nil once opened for a regular file
	isDir  bool
	opened bool
	dirBuf []byte // cached concatenated stat records for directory reads
}

// --- helpers ---

func rerr(tag uint16, msg string) proto.FCall {
	return &proto.RError{Header: proto.Header{Type: proto.Rerror, Tag: tag}, Ename: msg}
}

// joinPath joins a base namespace path with a single component, treating the
// root (".") correctly. wanix namespace paths do not start with "/".
func joinPath(base, name string) string {
	if name == "." || name == "" {
		return base
	}
	if base == "." || base == "" {
		return name
	}
	return pathpkg.Join(base, name)
}

// qidFor synthesizes a 9P qid for a namespace path. wanix has no inode, so the
// path is hashed into a 64-bit unique id; the qtype is the high bit for dirs.
func qidFor(p string, info fs.FileInfo) proto.Qid {
	qtype := uint8(0)
	if info.IsDir() {
		qtype = 0x80
	}
	h := fnv.New64a()
	h.Write([]byte(p))
	return proto.Qid{Qtype: qtype, Vers: uint32(info.ModTime().Unix()), Uid: h.Sum64()}
}

func statFor(p string, info fs.FileInfo) proto.Stat {
	mode := uint32(info.Mode().Perm())
	if info.IsDir() {
		mode |= proto.DMDIR
	}
	name := info.Name()
	if p == "." || name == "." {
		name = "/"
	}
	return proto.Stat{
		Qid:    qidFor(p, info),
		Mode:   mode,
		Atime:  uint32(info.ModTime().Unix()),
		Mtime:  uint32(info.ModTime().Unix()),
		Length: uint64(info.Size()),
		Name:   name,
		Uid:    "glenda",
		Gid:    "glenda",
		Muid:   "glenda",
	}
}

func (s *srv) stat(p string) (fs.FileInfo, error) {
	return fs.Stat(s.Fsys, p)
}

func (c *conn) fid(id uint32) (*fidState, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	f, ok := c.fids[id]
	return f, ok
}

// --- dispatch ---

func (s *srv) handle(c *conn, call proto.FCall) proto.FCall {
	switch t := call.(type) {
	case *proto.TRVersion:
		return s.version(c, t)
	case *proto.TAuth:
		return rerr(t.Tag, "authentication not required")
	case *proto.TAttach:
		return s.attach(c, t)
	case *proto.TFlush:
		return &proto.RFlush{Header: proto.Header{Type: proto.Rflush, Tag: t.Tag}}
	case *proto.TWalk:
		return s.walk(c, t)
	case *proto.TOpen:
		return s.open(c, t)
	case *proto.TCreate:
		return s.create(c, t)
	case *proto.TRead:
		return s.read(c, t)
	case *proto.TWrite:
		return s.write(c, t)
	case *proto.TClunk:
		return s.clunk(c, t)
	case *proto.TRemove:
		return s.remove(c, t)
	case *proto.TStat:
		return s.statHandler(c, t)
	case *proto.TWstat:
		return s.wstat(c, t)
	default:
		log.Printf("p9p: unhandled message %T", call)
		return rerr(call.GetTag(), "unhandled message")
	}
}

func (s *srv) version(c *conn, t *proto.TRVersion) proto.FCall {
	if t.Version != "9P2000" {
		return rerr(t.Tag, "unsupported version")
	}
	if t.Msize == 0 || t.Msize > proto.MaxMsgLen {
		t.Msize = proto.MaxMsgLen
	}
	c.msize = t.Msize
	return &proto.TRVersion{Header: proto.Header{Type: proto.Rversion, Tag: t.Tag}, Msize: t.Msize, Version: "9P2000"}
}

// ioUnit returns the iounit to advertise in Ropen/Rcreate: the largest read
// the client may issue without exceeding the negotiated msize. A 9P server
// MUST NOT send a message larger than msize, and iounit must be <= msize-11
// (size[4]+type[1]+tag[2]+count[4] header around the data). Returning a fixed
// proto.IOUnit larger than the client's msize makes a trusting client request
// that much data, the server returns an oversized Rread, and the client's
// msize-sized RPC buffer overflows -> stream desync / hang. Cap it here.
func (c *conn) ioUnit() uint32 {
	if c.msize <= 11 {
		return 0
	}
	u := c.msize - 11
	if u > proto.IOUnit {
		u = proto.IOUnit
	}
	return u
}

// readMax is the most data bytes a single Rread may carry for this conn.
func (c *conn) readMax() uint32 {
	if c.msize <= 11 {
		return proto.IOUnit
	}
	return c.msize - 11
}

func (s *srv) attach(c *conn, t *proto.TAttach) proto.FCall {
	info, err := s.stat(".")
	if err != nil {
		return rerr(t.Tag, "attach: "+err.Error())
	}
	c.mu.Lock()
	c.fids[t.Fid] = &fidState{path: ".", isDir: true}
	c.mu.Unlock()
	return &proto.RAttach{Header: proto.Header{Type: proto.Rattach, Tag: t.Tag}, Qid: qidFor(".", info)}
}

func (s *srv) walk(c *conn, t *proto.TWalk) proto.FCall {
	f, ok := c.fid(t.Fid)
	if !ok {
		return rerr(t.Tag, "unknown fid")
	}
	cur := f.path
	wqids := make([]proto.Qid, 0, len(t.Wname))
	for _, name := range t.Wname {
		np := joinPath(cur, name)
		info, err := s.stat(np)
		if err != nil {
			break // stop at first missing component
		}
		wqids = append(wqids, qidFor(np, info))
		cur = np
	}
	if len(wqids) > 0 || len(t.Wname) == 0 {
		isDir := false
		if info, err := s.stat(cur); err == nil {
			isDir = info.IsDir()
		}
		c.mu.Lock()
		c.fids[t.Newfid] = &fidState{path: cur, isDir: isDir}
		c.mu.Unlock()
	}
	return &proto.RWalk{Header: proto.Header{Type: proto.Rwalk, Tag: t.Tag}, Nwqid: uint16(len(wqids)), Wqid: wqids}
}

func (s *srv) open(c *conn, t *proto.TOpen) proto.FCall {
	f, ok := c.fid(t.Fid)
	if !ok {
		return rerr(t.Tag, "unknown fid")
	}
	info, err := s.stat(f.path)
	if err != nil {
		return rerr(t.Tag, err.Error())
	}
	if info.IsDir() {
		buf, err := s.dirListing(f.path)
		if err != nil {
			return rerr(t.Tag, err.Error())
		}
		c.mu.Lock()
		f.opened = true
		f.isDir = true
		f.dirBuf = buf
		c.mu.Unlock()
	} else {
		file, err := s.openFile(f.path, t.Mode)
		if err != nil {
			return rerr(t.Tag, err.Error())
		}
		c.mu.Lock()
		f.opened = true
		f.isDir = false
		f.file = file
		c.mu.Unlock()
	}
	return &proto.ROpen{Header: proto.Header{Type: proto.Ropen, Tag: t.Tag}, Qid: qidFor(f.path, info), Iounit: c.ioUnit()}
}

func (s *srv) create(c *conn, t *proto.TCreate) proto.FCall {
	f, ok := c.fid(t.Fid)
	if !ok {
		return rerr(t.Tag, "unknown fid")
	}
	np := joinPath(f.path, t.Name)
	if t.Perm&proto.DMDIR != 0 {
		if err := fs.Mkdir(s.Fsys, np, fs.FileMode(t.Perm&0777)); err != nil {
			return rerr(t.Tag, err.Error())
		}
		c.mu.Lock()
		f.path = np
		f.isDir = true
		c.mu.Unlock()
	} else {
		file, err := s.createFile(np, fs.FileMode(t.Perm&0777))
		if err != nil {
			return rerr(t.Tag, err.Error())
		}
		c.mu.Lock()
		f.opened = true
		f.isDir = false
		f.file = file
		f.path = np
		c.mu.Unlock()
	}
	info, err := s.stat(np)
	if err != nil {
		return rerr(t.Tag, err.Error())
	}
	return &proto.RCreate{Header: proto.Header{Type: proto.Rcreate, Tag: t.Tag}, Qid: qidFor(np, info), Iounit: c.ioUnit()}
}

func (s *srv) read(c *conn, t *proto.TRead) proto.FCall {
	f, ok := c.fid(t.Fid)
	if !ok || !f.opened {
		return rerr(t.Tag, "not open")
	}
	// Cap the data we return to readMax so the Rread never exceeds the
	// negotiated msize. A client that trusts our iounit (== readMax) won't
	// ask for more, but a client that ignores iounit and reads msize-11 or
	// the server's advertised count must still get a legal-sized reply.
	max := uint64(c.readMax())
	count := uint64(t.Count)
	if count > max {
		count = max
	}
	if f.isDir {
		buf := f.dirBuf
		if t.Offset >= uint64(len(buf)) {
			return &proto.RRead{Header: proto.Header{Type: proto.Rread, Tag: t.Tag}, Count: 0}
		}
		end := t.Offset + count
		if end > uint64(len(buf)) {
			end = uint64(len(buf))
		}
		data := buf[t.Offset:end]
		return &proto.RRead{Header: proto.Header{Type: proto.Rread, Tag: t.Tag}, Count: uint32(len(data)), Data: data}
	}
	p := make([]byte, count)
	n, err := fs.ReadAt(f.file, p, int64(t.Offset))
	if err != nil && !errors.Is(err, io.EOF) && n == 0 {
		return rerr(t.Tag, err.Error())
	}
	return &proto.RRead{Header: proto.Header{Type: proto.Rread, Tag: t.Tag}, Count: uint32(n), Data: p[:n]}
}

func (s *srv) write(c *conn, t *proto.TWrite) proto.FCall {
	f, ok := c.fid(t.Fid)
	if !ok || !f.opened || f.isDir {
		return rerr(t.Tag, "not open for write")
	}
	n, err := fs.WriteAt(f.file, t.Data, int64(t.Offset))
	if err != nil {
		return rerr(t.Tag, err.Error())
	}
	return &proto.RWrite{Header: proto.Header{Type: proto.Rwrite, Tag: t.Tag}, Count: uint32(n)}
}

func (s *srv) clunk(c *conn, t *proto.TClunk) proto.FCall {
	c.mu.Lock()
	f, ok := c.fids[t.Fid]
	if ok {
		delete(c.fids, t.Fid)
	}
	c.mu.Unlock()
	if ok && f.file != nil {
		f.file.Close()
	}
	return &proto.RClunk{Header: proto.Header{Type: proto.Rclunk, Tag: t.Tag}}
}

func (s *srv) remove(c *conn, t *proto.TRemove) proto.FCall {
	f, ok := c.fid(t.Fid)
	if !ok {
		return rerr(t.Tag, "unknown fid")
	}
	if err := fs.Remove(s.Fsys, f.path); err != nil {
		return rerr(t.Tag, err.Error())
	}
	c.mu.Lock()
	delete(c.fids, t.Fid)
	c.mu.Unlock()
	if f.file != nil {
		f.file.Close()
	}
	return &proto.RRemove{Header: proto.Header{Type: proto.Rremove, Tag: t.Tag}}
}

func (s *srv) statHandler(c *conn, t *proto.TStat) proto.FCall {
	f, ok := c.fid(t.Fid)
	if !ok {
		return rerr(t.Tag, "unknown fid")
	}
	info, err := s.stat(f.path)
	if err != nil {
		return rerr(t.Tag, err.Error())
	}
	return &proto.RStat{Header: proto.Header{Type: proto.Rstat, Tag: t.Tag}, Stat: statFor(f.path, info)}
}

func (s *srv) wstat(c *conn, t *proto.TWstat) proto.FCall {
	f, ok := c.fid(t.Fid)
	if !ok {
		return rerr(t.Tag, "unknown fid")
	}
	st := t.Stat
	if st.Name != "" && st.Name != "/" {
		np := joinPath(pathpkg.Dir(f.path), st.Name)
		if np != f.path {
			if err := fs.Rename(s.Fsys, f.path, np); err == nil {
				c.mu.Lock()
				f.path = np
				c.mu.Unlock()
			}
		}
	}
	if st.Mode != 0 {
		_ = fs.Chmod(s.Fsys, f.path, fs.FileMode(st.Mode&0777)) // best-effort
	}
	if st.Length != 0 {
		_ = fs.Truncate(s.Fsys, f.path, int64(st.Length))
	}
	return &proto.RWstat{Header: proto.Header{Type: proto.Rwstat, Tag: t.Tag}}
}

// dirListing returns the concatenated 9P stat records for the directory's
// children (the data returned by reading a directory).
func (s *srv) dirListing(dir string) ([]byte, error) {
	entries, err := fs.ReadDir(s.Fsys, dir)
	if err != nil {
		return nil, err
	}
	var buf []byte
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		child := joinPath(dir, e.Name())
		st := statFor(child, info)
		buf = append(buf, st.Compose()...)
	}
	return buf, nil
}

// openFile opens an existing file honoring the 9P open mode. It uses the
// namespace's canonical OpenContext (the same path Stat/ReadDir use), so the
// handle shares data with the rest of the namespace. OpenContext ignores the
// mode; Otrunc is applied separately.
func (s *srv) openFile(p string, mode proto.Mode) (fs.File, error) {
	f, err := fs.OpenContext(context.Background(), s.Fsys, p)
	if err != nil {
		return nil, err
	}
	if mode&proto.Otrunc != 0 {
		_ = fs.Truncate(s.Fsys, p, 0)
	}
	return f, nil
}

// createFile creates and opens a regular file, rejecting an existing entry
// (9P create is exclusive). It uses the namespace's canonical Create, so the
// new file is visible to Stat/ReadDir.
func (s *srv) createFile(p string, perm fs.FileMode) (fs.File, error) {
	if _, err := fs.Stat(s.Fsys, p); err == nil {
		return nil, fs.ErrExist
	}
	f, err := fs.Create(s.Fsys, p)
	if err != nil {
		return nil, err
	}
	if perm != 0 {
		_ = fs.Chmod(s.Fsys, p, perm)
	}
	return f, nil
}
