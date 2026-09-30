package p9p

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/knusbaum/go9p/proto"
	"tractor.dev/wanix/fs"
)

// ClientFS mounts a 9P2000 filesystem for reading and writing. user and aname
// are sent in Tattach; authentication exchanges are not implemented here.
//
// Metadata changes are 9P2000 wstats, so they follow Plan 9's rules: a rename
// stays within its directory, and owners are names, so a numeric Chown is
// not supported. Plan 9 has no symbolic links.
func ClientFS(conn io.ReadWriteCloser, user, aname string) (fs.FS, error) {
	c := &clientFS{conn: conn, msize: 8192, nextFID: 1}
	fail := func(err error) (fs.FS, error) { conn.Close(); return nil, err }
	r, err := c.rpc(&proto.TRVersion{Header: proto.Header{Type: proto.Tversion, Tag: 0xffff}, Msize: c.msize, Version: "9P2000"})
	if err != nil {
		return fail(err)
	}
	version := r.(*proto.TRVersion)
	if version.Version != "9P2000" || version.Msize < 256 || version.Msize > c.msize {
		return fail(fmt.Errorf("unsupported 9P version/msize: %s/%d", version.Version, version.Msize))
	}
	c.msize = version.Msize
	if user == "" {
		user = "none"
	}
	_, err = c.rpc(&proto.TAttach{Header: proto.Header{Type: proto.Tattach}, Fid: 1, Afid: ^uint32(0), Uname: user, Aname: aname})
	if err != nil {
		return fail(err)
	}
	return c, nil
}

type clientFS struct {
	conn     io.ReadWriteCloser
	mu       sync.Mutex // Serializes RPCs on this import connection.
	nextFID  uint32
	nextTag  uint16
	msize    uint32
	terminal error
}

func (c *clientFS) Close() error { return c.conn.Close() }

func (c *clientFS) rpc(call proto.FCall) (proto.FCall, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.terminal != nil {
		return nil, c.terminal
	}
	packet := call.Compose()
	tag := call.GetTag()
	if packet[4] != proto.Tversion {
		c.nextTag++
		if c.nextTag == 0xffff {
			c.nextTag = 0
		}
		tag = c.nextTag
		binary.LittleEndian.PutUint16(packet[5:7], tag)
	}
	n, err := c.conn.Write(packet)
	if err == nil && n != len(packet) {
		err = io.ErrShortWrite
	}
	var resp proto.FCall
	if err == nil {
		resp, err = proto.ParseCall(c.conn)
	}
	if err != nil {
		c.terminal = err
		c.conn.Close()
		return nil, err
	}
	if resp.GetTag() != tag {
		c.terminal = errors.New("9P response tag mismatch")
		c.conn.Close()
		return nil, c.terminal
	}
	if e, ok := resp.(*proto.RError); ok {
		return nil, errors.New(e.Ename)
	}
	if resp.Compose()[4] != packet[4]+1 {
		c.terminal = fmt.Errorf("unexpected 9P response %T", resp)
		c.conn.Close()
		return nil, c.terminal
	}
	return resp, nil
}

func (c *clientFS) walk(name string) (uint32, error) {
	if !fs.ValidPath(name) {
		return 0, fs.ErrInvalid
	}
	c.mu.Lock()
	c.nextFID++
	id := c.nextFID
	c.mu.Unlock()
	names := strings.Split(name, "/")
	if name == "." {
		names = nil
	}
	if len(names) > 16 {
		return 0, errors.New("9P walk exceeds 16 components")
	}
	r, err := c.rpc(&proto.TWalk{Header: proto.Header{Type: proto.Twalk}, Fid: 1, Newfid: id, Nwname: uint16(len(names)), Wname: names})
	if err != nil {
		return 0, err
	}
	if int(r.(*proto.RWalk).Nwqid) != len(names) {
		c.clunk(id)
		return 0, fs.ErrNotExist
	}
	return id, nil
}

func (c *clientFS) clunk(id uint32) error {
	_, err := c.rpc(&proto.TClunk{Header: proto.Header{Type: proto.Tclunk}, Fid: id})
	return err
}
func (c *clientFS) statFID(id uint32) (fs.FileInfo, error) {
	r, err := c.rpc(&proto.TStat{Header: proto.Header{Type: proto.Tstat}, Fid: id})
	if err != nil {
		return nil, err
	}
	return remoteInfo{r.(*proto.RStat).Stat}, nil
}
func (c *clientFS) Stat(name string) (fs.FileInfo, error) {
	id, err := c.walk(name)
	if err != nil {
		return nil, &fs.PathError{Op: "stat", Path: name, Err: err}
	}
	defer c.clunk(id)
	return c.statFID(id)
}
func (c *clientFS) Open(name string) (fs.File, error) {
	id, err := c.walk(name)
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: name, Err: err}
	}
	info, err := c.statFID(id)
	if err != nil {
		c.clunk(id)
		return nil, err
	}
	r, err := c.rpc(&proto.TOpen{Header: proto.Header{Type: proto.Topen}, Fid: id, Mode: proto.Oread})
	if err != nil {
		c.clunk(id)
		return nil, err
	}
	return c.newFile(id, info, r.(*proto.ROpen).Iounit, proto.Oread, 0), nil
}

// newFile wraps an opened fid. The server's iounit is capped so neither an
// Rread reply (11 bytes of header) nor a Twrite request (23 bytes) can exceed
// the negotiated msize.
func (c *clientFS) newFile(id uint32, info fs.FileInfo, iounit uint32, mode proto.Mode, flag int) *clientFile {
	max := c.msize - 23
	if iounit == 0 || iounit > max {
		iounit = max
	}
	access := mode & 3
	return &clientFile{
		client:    c,
		fid:       id,
		info:      info,
		iounit:    iounit,
		writable:  access == proto.Owrite || access == proto.Ordwr,
		appending: flag&os.O_APPEND != 0,
	}
}

// OpenFile opens name with os-style flags, creating it in its parent
// directory when O_CREATE is set and it does not exist.
func (c *clientFS) OpenFile(name string, flag int, perm fs.FileMode) (fs.File, error) {
	mode := openMode(flag)
	id, err := c.walk(name)
	if err != nil {
		if flag&os.O_CREATE == 0 {
			return nil, &fs.PathError{Op: "open", Path: name, Err: err}
		}
		return c.create(name, perm, mode, flag)
	}
	if flag&os.O_CREATE != 0 && flag&os.O_EXCL != 0 {
		c.clunk(id)
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrExist}
	}
	info, err := c.statFID(id)
	if err != nil {
		c.clunk(id)
		return nil, &fs.PathError{Op: "open", Path: name, Err: err}
	}
	r, err := c.rpc(&proto.TOpen{Header: proto.Header{Type: proto.Topen}, Fid: id, Mode: mode})
	if err != nil {
		c.clunk(id)
		return nil, &fs.PathError{Op: "open", Path: name, Err: err}
	}
	return c.newFile(id, info, r.(*proto.ROpen).Iounit, mode, flag), nil
}

func (c *clientFS) Create(name string) (fs.File, error) {
	return c.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0666)
}

// create issues Tcreate in name's parent directory. On success the parent's
// fid becomes the new file, already open with mode.
func (c *clientFS) create(name string, perm fs.FileMode, mode proto.Mode, flag int) (fs.File, error) {
	dir, base := path.Split(name)
	dir = strings.TrimSuffix(dir, "/")
	if dir == "" {
		dir = "."
	}
	if base == "" || base == "." || base == ".." {
		return nil, &fs.PathError{Op: "create", Path: name, Err: fs.ErrInvalid}
	}
	id, err := c.walk(dir)
	if err != nil {
		return nil, &fs.PathError{Op: "create", Path: name, Err: err}
	}
	p := uint32(perm & 0777)
	if perm&fs.ModeDir != 0 {
		p |= proto.DMDIR
	}
	r, err := c.rpc(&proto.TCreate{Header: proto.Header{Type: proto.Tcreate}, Fid: id, Name: base, Perm: p, Mode: uint8(mode)})
	if err != nil {
		c.clunk(id)
		return nil, &fs.PathError{Op: "create", Path: name, Err: err}
	}
	info, err := c.statFID(id)
	if err != nil {
		c.clunk(id)
		return nil, &fs.PathError{Op: "create", Path: name, Err: err}
	}
	return c.newFile(id, info, r.(*proto.RCreate).Iounit, mode, flag), nil
}

// Mkdir creates a directory. Plan 9 creates directories open for reading.
func (c *clientFS) Mkdir(name string, perm fs.FileMode) error {
	f, err := c.create(name, perm|fs.ModeDir, proto.Oread, 0)
	if err != nil {
		return err
	}
	return f.Close()
}

// Remove deletes name. Tremove clunks its fid whether or not it succeeds.
func (c *clientFS) Remove(name string) error {
	id, err := c.walk(name)
	if err != nil {
		return &fs.PathError{Op: "remove", Path: name, Err: err}
	}
	if _, err := c.rpc(&proto.TRemove{Header: proto.Header{Type: proto.Tremove}, Fid: id}); err != nil {
		return &fs.PathError{Op: "remove", Path: name, Err: err}
	}
	return nil
}

// Rename changes a name within its directory, the only rename 9P2000 has.
func (c *clientFS) Rename(oldname, newname string) error {
	if path.Dir(oldname) != path.Dir(newname) {
		return &fs.PathError{Op: "rename", Path: oldname, Err: errors.New("9P2000 renames only within a directory")}
	}
	st := dontTouch()
	st.Name = path.Base(newname)
	return c.wstat("rename", oldname, st)
}

// Chmod replaces the permission bits and keeps the rest of the Plan 9 mode,
// such as DMDIR and DMAPPEND.
func (c *clientFS) Chmod(name string, mode fs.FileMode) error {
	info, err := c.Stat(name)
	if err != nil {
		return err
	}
	current, _ := info.Sys().(proto.Stat)
	st := dontTouch()
	st.Mode = current.Mode&^0777 | uint32(mode&0777)
	return c.wstat("chmod", name, st)
}

func (c *clientFS) Truncate(name string, size int64) error {
	if size < 0 {
		return &fs.PathError{Op: "truncate", Path: name, Err: fs.ErrInvalid}
	}
	st := dontTouch()
	st.Length = uint64(size)
	return c.wstat("truncate", name, st)
}

// Chtimes sets the modification time; 9P2000 gives clients no way to set
// the access time.
func (c *clientFS) Chtimes(name string, atime, mtime time.Time) error {
	st := dontTouch()
	st.Mtime = uint32(mtime.Unix())
	return c.wstat("chtimes", name, st)
}

// Chown is not supported: 9P2000 owners are names, not numbers.
func (c *clientFS) Chown(name string, uid, gid int) error {
	return &fs.PathError{Op: "chown", Path: name, Err: fs.ErrNotSupported}
}

func (c *clientFS) wstat(op, name string, st proto.Stat) error {
	id, err := c.walk(name)
	if err != nil {
		return &fs.PathError{Op: op, Path: name, Err: err}
	}
	defer c.clunk(id)
	if _, err := c.rpc(&proto.TWstat{Header: proto.Header{Type: proto.Twstat}, Fid: id, Stat: st}); err != nil {
		return &fs.PathError{Op: op, Path: name, Err: err}
	}
	return nil
}

// dontTouch is a stat whose every field means "leave unchanged" in a
// Twstat: all ones in numbers and empty strings (stat(5)).
func dontTouch() proto.Stat {
	return proto.Stat{
		Type:   ^uint16(0),
		Dev:    ^uint32(0),
		Qid:    proto.Qid{Qtype: ^uint8(0), Vers: ^uint32(0), Uid: ^uint64(0)},
		Mode:   ^uint32(0),
		Atime:  ^uint32(0),
		Mtime:  ^uint32(0),
		Length: ^uint64(0),
	}
}

func openMode(flag int) proto.Mode {
	var m proto.Mode
	switch flag & (os.O_RDONLY | os.O_WRONLY | os.O_RDWR) {
	case os.O_WRONLY:
		m = proto.Owrite
	case os.O_RDWR:
		m = proto.Ordwr
	default:
		m = proto.Oread
	}
	if flag&os.O_TRUNC != 0 {
		m |= proto.Otrunc
	}
	return m
}

type clientFile struct {
	client    *clientFS
	fid       uint32
	info      fs.FileInfo
	iounit    uint32
	offset    uint64
	closed    bool
	writable  bool
	appending bool
	mu        sync.Mutex
	entries   []fs.DirEntry
	listed    bool
	index     int
}

func (f *clientFile) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return 0, fs.ErrClosed
	}
	if !f.writable {
		return 0, errors.New("9P file not open for writing")
	}
	if f.appending {
		info, err := f.client.statFID(f.fid)
		if err != nil {
			return 0, err
		}
		f.offset = uint64(info.Size())
	}
	n, err := f.writeAt(p, f.offset)
	f.offset += uint64(n)
	return n, err
}

func (f *clientFile) WriteAt(p []byte, off int64) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return 0, fs.ErrClosed
	}
	if !f.writable {
		return 0, errors.New("9P file not open for writing")
	}
	if off < 0 {
		return 0, fs.ErrInvalid
	}
	return f.writeAt(p, uint64(off))
}

// writeAt sends p in iounit-sized Twrites. A server that accepts nothing is
// a short write, not a loop.
func (f *clientFile) writeAt(p []byte, off uint64) (int, error) {
	total := 0
	for total < len(p) {
		chunk := p[total:]
		if uint64(len(chunk)) > uint64(f.iounit) {
			chunk = chunk[:f.iounit]
		}
		r, err := f.client.rpc(&proto.TWrite{Header: proto.Header{Type: proto.Twrite}, Fid: f.fid, Offset: off + uint64(total), Count: uint32(len(chunk)), Data: chunk})
		if err != nil {
			return total, err
		}
		n := int(r.(*proto.RWrite).Count)
		if n > len(chunk) {
			return total, errors.New("invalid 9P write count")
		}
		total += n
		if n == 0 {
			return total, io.ErrShortWrite
		}
	}
	return total, nil
}

func (f *clientFile) Seek(offset int64, whence int) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return 0, fs.ErrClosed
	}
	var base int64
	switch whence {
	case io.SeekStart:
	case io.SeekCurrent:
		base = int64(f.offset)
	case io.SeekEnd:
		info, err := f.client.statFID(f.fid)
		if err != nil {
			return 0, err
		}
		base = info.Size()
	default:
		return 0, fs.ErrInvalid
	}
	if base+offset < 0 {
		return 0, fs.ErrInvalid
	}
	f.offset = uint64(base + offset)
	return int64(f.offset), nil
}

func (f *clientFile) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return fs.ErrClosed
	}
	f.closed = true
	return f.client.clunk(f.fid)
}
func (f *clientFile) Stat() (fs.FileInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return nil, fs.ErrClosed
	}
	return f.client.statFID(f.fid)
}
func (f *clientFile) readAt(p []byte, off uint64) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	count := len(p)
	if uint64(count) > uint64(f.iounit) {
		count = int(f.iounit)
	}
	r, err := f.client.rpc(&proto.TRead{Header: proto.Header{Type: proto.Tread}, Fid: f.fid, Offset: off, Count: uint32(count)})
	if err != nil {
		return 0, err
	}
	read := r.(*proto.RRead)
	if read.Count != uint32(len(read.Data)) || len(read.Data) > count {
		return 0, errors.New("invalid 9P read count")
	}
	n := copy(p, read.Data)
	if n == 0 {
		return 0, io.EOF
	}
	return n, nil
}
func (f *clientFile) Read(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return 0, fs.ErrClosed
	}
	n, err := f.readAt(p, f.offset)
	f.offset += uint64(n)
	return n, err
}
func (f *clientFile) ReadAt(p []byte, off int64) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return 0, fs.ErrClosed
	}
	if off < 0 || f.info.IsDir() {
		return 0, fs.ErrInvalid
	}
	total := 0
	for total < len(p) {
		n, err := f.readAt(p[total:], uint64(off)+uint64(total))
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}
func (f *clientFile) ReadDir(count int) ([]fs.DirEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return nil, fs.ErrClosed
	}
	if !f.info.IsDir() {
		return nil, fs.ErrInvalid
	}
	if !f.listed {
		var entries []fs.DirEntry
		off := uint64(0)
		for {
			buf := make([]byte, f.iounit)
			n, err := f.readAt(buf, off)
			if err == io.EOF {
				break
			}
			if err != nil {
				return nil, err
			}
			records := buf[:n]
			for len(records) > 0 {
				if len(records) < 2 {
					return nil, errors.New("split 9P directory record")
				}
				size := int(binary.LittleEndian.Uint16(records)) + 2
				if size < 49 || size > len(records) {
					return nil, errors.New("invalid 9P directory record")
				}
				record := records[:size]
				pos := 41
				for i := 0; i < 4; i++ {
					if pos+2 > size {
						return nil, errors.New("invalid 9P directory string")
					}
					length := int(binary.LittleEndian.Uint16(record[pos:]))
					pos += 2 + length
					if pos > size {
						return nil, errors.New("invalid 9P directory string")
					}
				}
				if pos != size {
					return nil, errors.New("invalid 9P directory size")
				}
				stats, err := proto.ParseStats(record)
				if err != nil {
					return nil, err
				}
				entries = append(entries, remoteInfo{stats[0]})
				records = records[size:]
			}
			off += uint64(n)
		}
		f.entries = entries
		f.listed = true
	}
	end := len(f.entries)
	if count > 0 && count < end-f.index {
		end = f.index + count
	}
	result := f.entries[f.index:end]
	f.index = end
	if count > 0 && len(result) == 0 {
		return nil, io.EOF
	}
	return result, nil
}

type remoteInfo struct{ stat proto.Stat }

func (i remoteInfo) Name() string { return i.stat.Name }
func (i remoteInfo) Size() int64  { return int64(i.stat.Length) }
func (i remoteInfo) Mode() fs.FileMode {
	mode := fs.FileMode(i.stat.Mode & 0777)
	if i.stat.Mode&proto.DMDIR != 0 {
		mode |= fs.ModeDir
	}
	return mode
}
func (i remoteInfo) ModTime() time.Time         { return time.Unix(int64(i.stat.Mtime), 0) }
func (i remoteInfo) IsDir() bool                { return i.stat.Mode&proto.DMDIR != 0 }
func (i remoteInfo) Sys() any                   { return i.stat }
func (i remoteInfo) Type() fs.FileMode          { return i.Mode().Type() }
func (i remoteInfo) Info() (fs.FileInfo, error) { return i, nil }
