package fskit

import (
	"context"
	"io"
	"log/slog"
	"path"
	"sync"
	"sync/atomic"
	"time"

	"tractor.dev/wanix/fs"
)

var nextNodeID atomic.Uint64

// Node is used to create an fs.FileInfo, fs.DirEntry, or fs.FS.
type Node struct {
	id      uint64
	version uint32
	path    string
	mode    fs.FileMode
	modTime time.Time
	size    int64
	sys     any
	uid     int
	gid     int
	data    []byte
	log     *slog.Logger

	reader io.Reader
	writer io.Writer

	mu sync.Mutex
}

func RawNode(attrs ...any) *Node {
	n := &Node{id: nextNodeID.Add(1), version: 1}
	for _, m := range attrs {
		switch v := m.(type) {
		case *Node:
			v.mu.Lock()
			n.id = v.id
			n.version = v.version
			n.path = v.path
			n.mode = v.mode
			n.size = v.size
			n.modTime = v.modTime
			n.sys = v.sys
			n.uid = v.uid
			n.gid = v.gid
			n.data = append([]byte(nil), v.data...)
			n.reader = v.reader
			n.writer = v.writer
			n.log = v.log
			v.mu.Unlock()
		case int64:
			n.size = v
		case int:
			n.size = int64(v)
		case uint64:
			n.size = int64(v)
		case time.Time:
			n.modTime = v
		case []byte:
			n.data = append([]byte(nil), v...)
		case string:
			n.path = v
		case fs.FileMode:
			n.mode = v
		case fs.FileInfo:
			n.id = 0 // Metadata alone cannot establish a native identity.
			n.path = v.Name()
			n.mode = v.Mode()
			n.size = v.Size()
			n.modTime = v.ModTime()
			n.sys = v.Sys()
			if ident, ok := v.(interface{ FileIdentity() (uint64, uint32) }); ok {
				n.id, n.version = ident.FileIdentity()
			}

		// these must come after fs.FileInfo since
		// some of our fs.FileInfo implementations
		// are also io.Readers...
		case io.ReadWriter:
			n.reader = v
			n.writer = v
		case io.Reader:
			n.reader = v
		case io.Writer:
			n.writer = v
		case *slog.Logger:
			n.log = v

		}
	}
	if n.log == nil {
		n.log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return n
}

// CloneNode creates an independent file with a fresh identity. RawNode copies
// preserve identity because they are also used for metadata/name wrappers.
func CloneNode(n *Node) *Node {
	copy := RawNode(n)
	copy.id = nextNodeID.Add(1)
	copy.version = 1
	return copy
}

func Entry(name string, mode fs.FileMode, more ...any) *Node {
	n := RawNode(more...)
	n.path = name
	n.mode = mode
	return n
}

// fs.FileInfo and fs.DirEntry interfaces implemented
var _ = (fs.FileInfo)((*Node)(nil))
var _ = (fs.DirEntry)((*Node)(nil))

func (n *Node) Info() (fs.FileInfo, error) { return n, nil }

func (n *Node) Path() string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.path
}

func (n *Node) Name() string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return path.Base(n.path)
}

func (n *Node) Mode() fs.FileMode {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.mode
}

func (n *Node) Type() fs.FileMode {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.mode.Type()
}

func (n *Node) ModTime() time.Time {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.modTime
}

func (n *Node) IsDir() bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.mode&fs.ModeDir != 0
}

func (n *Node) Sys() any {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.sys
}

func (n *Node) Size() int64 {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.size < 0 {
		return 0
	}
	if n.size > 0 {
		return n.size
	}
	return int64(len(n.data))
}

func (n *Node) String() string {
	return fs.FormatFileInfo(n)
}

func (n *Node) Data() []byte {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.data == nil {
		return nil
	}
	dataCopy := make([]byte, len(n.data))
	copy(dataCopy, n.data)
	return dataCopy
}

func (n *Node) Log() *slog.Logger {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.log
}

func (n *Node) Reader() io.Reader {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.reader
}

func (n *Node) Writer() io.Writer {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.writer
}

func SetLogger(n *Node, log *slog.Logger) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.log = log
}

func SetName(n *Node, name string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.path != name {
		n.path = name
		n.version++
	}
}

func SetData(n *Node, data []byte) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.data = append([]byte(nil), data...)
	n.size = 0
	n.changedLocked()
}

func SetMode(n *Node, mode fs.FileMode) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.mode != mode {
		n.mode = mode
		n.version++
	}
}

func SetModTime(n *Node, modTime time.Time) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.modTime = modTime
	n.version++
}

func SetSize(n *Node, size int64) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.size != size {
		n.size = size
		n.version++
	}
}

func SetSys(n *Node, sys any) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.sys = sys
}

func SetUid(n *Node, uid int) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.uid = uid
}

func SetGid(n *Node, gid int) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.gid = gid
}

func (n *Node) GetUID() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.uid
}

func (n *Node) GetGID() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.gid
}

// FileIdentity is stable across renames and metadata wrappers. Every content
// mutation increments the version, independently of wall-clock resolution.
func (n *Node) FileIdentity() (uint64, uint32) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.id, n.version
}

func (n *Node) changedLocked() {
	n.modTime = time.Now()
	n.version++
}

// Truncate updates data and metadata under the same lock as handle writes.
func (n *Node) Truncate(size int64) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if size < 0 || uint64(size) > uint64(^uint(0)>>1) {
		return fs.ErrInvalid
	}
	data := make([]byte, int(size))
	copy(data, n.data)
	n.data = data
	n.size = 0
	n.changedLocked()
	return nil
}

var _ fs.OpenContextFS = (*Node)(nil)

func (n *Node) Open(name string) (fs.File, error) {
	return n.OpenContext(context.Background(), name)
}

func (n *Node) OpenContext(ctx context.Context, name string) (fs.File, error) {
	if name == "." {
		return n.openFile(), nil
	}
	return nil, fs.ErrNotExist
}

func (n *Node) openFile() *nodeFile { return &nodeFile{inode: n} }

// Handles own only their cursor and lifetime; contents belong to the node.
type nodeFile struct {
	inode  *Node
	offset int64
	closed bool
	mu     sync.Mutex
}

func (f *nodeFile) Close() (err error) {
	defer func() { f.inode.Log().Debug("close", "name", f.inode.Path(), "err", err) }()
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return fs.ErrClosed
	}
	f.closed = true
	return nil
}

func (f *nodeFile) Stat() (fs.FileInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return nil, fs.ErrClosed
	}
	return f.inode, nil
}

func (f *nodeFile) Read(b []byte) (n int, err error) {
	defer func() { f.inode.Log().Debug("read", "name", f.inode.Path(), "n", n, "err", err) }()
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return 0, fs.ErrClosed
	}
	if r := f.inode.Reader(); r != nil {
		return r.Read(b)
	}
	f.inode.mu.Lock()
	defer f.inode.mu.Unlock()
	n, err = f.readLocked(b, f.offset)
	f.offset += int64(n)
	if n > 0 {
		err = nil
	}
	return n, err
}

func (f *nodeFile) ReadAt(b []byte, offset int64) (n int, err error) {
	defer func() { f.inode.Log().Debug("readat", "name", f.inode.Path(), "offset", offset, "n", n, "err", err) }()
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return 0, fs.ErrClosed
	}
	if r := f.inode.Reader(); r != nil {
		return r.Read(b)
	}
	f.inode.mu.Lock()
	defer f.inode.mu.Unlock()
	return f.readLocked(b, offset)
}

func (f *nodeFile) readLocked(b []byte, offset int64) (int, error) {
	if offset < 0 {
		return 0, fs.ErrInvalid
	}
	if len(b) == 0 {
		return 0, nil
	}
	if offset >= int64(len(f.inode.data)) {
		return 0, io.EOF
	}
	n := copy(b, f.inode.data[int(offset):])
	if n < len(b) {
		return n, io.EOF
	}
	return n, nil
}

func (f *nodeFile) Seek(offset int64, whence int) (newPos int64, err error) {
	defer func() {
		f.inode.Log().Debug("seek", "name", f.inode.Path(), "offset", offset, "whence", whence, "newPos", newPos, "err", err)
	}()
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return 0, fs.ErrClosed
	}
	f.inode.mu.Lock()
	defer f.inode.mu.Unlock()
	switch whence {
	case io.SeekStart:
	case io.SeekCurrent:
		offset += f.offset
	case io.SeekEnd:
		offset += int64(len(f.inode.data))
	default:
		return 0, fs.ErrInvalid
	}
	if offset < 0 || offset > int64(len(f.inode.data)) {
		return 0, fs.ErrInvalid
	}
	f.offset = offset
	return offset, nil
}

func (f *nodeFile) Write(b []byte) (n int, err error) {
	defer func() { f.inode.Log().Debug("write", "name", f.inode.Path(), "n", n, "err", err) }()
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return 0, fs.ErrClosed
	}
	if w := f.inode.Writer(); w != nil {
		return w.Write(b)
	}
	f.inode.mu.Lock()
	defer f.inode.mu.Unlock()
	n, err = f.writeLocked(b, f.offset)
	f.offset += int64(n)
	return n, err
}

func (f *nodeFile) WriteAt(b []byte, offset int64) (n int, err error) {
	defer func() { f.inode.Log().Debug("writeat", "name", f.inode.Path(), "offset", offset, "n", n, "err", err) }()
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return 0, fs.ErrClosed
	}
	if w := f.inode.Writer(); w != nil {
		return w.Write(b)
	}
	f.inode.mu.Lock()
	defer f.inode.mu.Unlock()
	if offset > int64(len(f.inode.data)) {
		return 0, fs.ErrInvalid
	}
	return f.writeLocked(b, offset)
}

func (f *nodeFile) writeLocked(b []byte, offset int64) (int, error) {
	const maxInt = int(^uint(0) >> 1)
	if offset < 0 || offset > int64(maxInt-len(b)) {
		return 0, fs.ErrInvalid
	}
	if len(b) == 0 {
		return 0, nil
	}
	end := int(offset) + len(b)
	if end > len(f.inode.data) {
		data := make([]byte, end)
		copy(data, f.inode.data)
		f.inode.data = data
	}
	copy(f.inode.data[int(offset):], b)
	f.inode.size = 0
	f.inode.changedLocked()
	return len(b), nil
}
