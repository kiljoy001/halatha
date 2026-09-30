// Package ipfsfs exposes IPFS as an fs.FS so it can be mounted into a wanix
// namespace and used with ordinary file operations:
//
//	cat /n/host/ipfs/<cid>/readme
//	ls  /n/host/ipfs/<cid>/
//	cp  file /n/host/ipfs/local/     (adds + pins)
//
// This replaces a control-file protocol (write "get <cid>" to ctl, poll a
// status file, read a data file) with reads and writes, which is the point
// of 9P.
//
// It talks to a Kubo node's HTTP API rather than a public gateway, because a
// gateway cannot support a filesystem: a directory CID returns a rendered
// HTML index page, not a listing, so ReadDir is impossible. Kubo's
// /api/v0/ls returns JSON with a name, size and type per entry. Kubo also
// allows pinning, so content written here persists instead of being evicted
// from someone else's cache.
//
// Layout:
//
//	/                 synthetic root: lists "local" plus any pinned CIDs
//	/<cid>            a file, or a directory to walk into
//	/<cid>/<path>     any path inside a directory CID
//	/local            recursive pins, by CID; writing a file here adds+pins it
package ipfsfs

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	"tractor.dev/wanix/fs"
	"tractor.dev/wanix/fs/fskit"
)

// DefaultAPI is the Kubo HTTP API address used when none is given.
const DefaultAPI = "http://127.0.0.1:5001"

// localDir is the synthetic directory holding recursive pins. Writing into
// it adds and pins content.
const localDir = "local"

// FS is an IPFS filesystem backed by a Kubo node's HTTP API.
//
// The zero value is not usable; call New.
type FS struct {
	api    string
	client *http.Client

	// statCache memoises files/stat lookups. CIDs are immutable, so a stat
	// for a given path can never become stale; without this, walking a
	// directory costs one API round trip per entry per operation.
	mu        sync.RWMutex
	statCache map[string]*statInfo
}

// New returns an FS talking to the Kubo API at apiAddr (DefaultAPI if empty).
func New(apiAddr string, client *http.Client) *FS {
	if apiAddr == "" {
		apiAddr = DefaultAPI
	}
	if client == nil {
		// Generous: a cold CID may need a DHT lookup plus a fetch from a
		// remote peer. The node answers from its own store immediately.
		client = &http.Client{Timeout: 60 * time.Second}
	}
	return &FS{
		api:       strings.TrimSuffix(apiAddr, "/"),
		client:    client,
		statCache: make(map[string]*statInfo),
	}
}

// statInfo is the decoded result of /api/v0/files/stat.
type statInfo struct {
	Hash           string
	Size           int64
	CumulativeSize int64
	Type           string // "file" or "directory"
}

func (s *statInfo) isDir() bool { return s.Type == "directory" }

// lsLink is one entry from /api/v0/ls.
type lsLink struct {
	Name string
	Hash string
	Size int64
	Type int // 1 = directory, 2 = file
}

type lsResponse struct {
	Objects []struct {
		Hash  string
		Links []lsLink
	}
}

// post calls a Kubo API endpoint. The Kubo API is POST-only; a GET returns
// 405 with a message about cross-origin attacks, which is easy to misread as
// an auth problem.
func (fsys *FS) post(ctx context.Context, endpoint string, args url.Values) ([]byte, error) {
	u := fmt.Sprintf("%s/api/v0/%s", fsys.api, endpoint)
	if len(args) > 0 {
		u += "?" + args.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := fsys.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		// Kubo reports "file does not exist" / "no link named" for a missing
		// path. Map those onto fs.ErrNotExist so callers (and 9P clients)
		// see a normal ENOENT instead of an opaque API error.
		msg := string(body)
		if strings.Contains(msg, "does not exist") ||
			strings.Contains(msg, "no link named") ||
			strings.Contains(msg, "not found") {
			return nil, fs.ErrNotExist
		}
		return nil, fmt.Errorf("ipfs %s: %s: %s", endpoint, resp.Status, truncate(msg, 200))
	}
	return body, nil
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// statPath stats an IPFS path ("<cid>" or "<cid>/a/b"), with caching.
func (fsys *FS) statPath(ctx context.Context, name string) (*statInfo, error) {
	fsys.mu.RLock()
	if st, ok := fsys.statCache[name]; ok {
		fsys.mu.RUnlock()
		return st, nil
	}
	fsys.mu.RUnlock()

	body, err := fsys.post(ctx, "files/stat", url.Values{"arg": {"/ipfs/" + name}})
	if err != nil {
		return nil, err
	}
	var st statInfo
	if err := json.Unmarshal(body, &st); err != nil {
		return nil, fmt.Errorf("ipfs stat %s: %w", name, err)
	}

	fsys.mu.Lock()
	fsys.statCache[name] = &st
	fsys.mu.Unlock()
	return &st, nil
}

// list returns the entries of a directory CID path.
func (fsys *FS) list(ctx context.Context, name string) ([]lsLink, error) {
	body, err := fsys.post(ctx, "ls", url.Values{"arg": {"/ipfs/" + name}})
	if err != nil {
		return nil, err
	}
	var out lsResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("ipfs ls %s: %w", name, err)
	}
	if len(out.Objects) == 0 {
		return nil, nil
	}
	return out.Objects[0].Links, nil
}

// cat reads the full contents of an IPFS file path.
func (fsys *FS) cat(ctx context.Context, name string) ([]byte, error) {
	return fsys.post(ctx, "cat", url.Values{"arg": {"/ipfs/" + name}})
}

// pins returns the recursively pinned CIDs.
func (fsys *FS) pins(ctx context.Context) ([]string, error) {
	body, err := fsys.post(ctx, "pin/ls", url.Values{"type": {"recursive"}})
	if err != nil {
		return nil, err
	}
	var out struct {
		Keys map[string]struct{ Type string }
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("ipfs pin/ls: %w", err)
	}
	cids := make([]string, 0, len(out.Keys))
	for cid := range out.Keys {
		cids = append(cids, cid)
	}
	return cids, nil
}

// entryMode renders an ls Type as a file mode.
func entryMode(typ int) fs.FileMode {
	if typ == 1 {
		return fs.ModeDir | 0555
	}
	return 0444
}

var _ fs.FS = (*FS)(nil)
var _ fs.OpenContextFS = (*FS)(nil)

func (fsys *FS) Open(name string) (fs.File, error) {
	return fsys.OpenContext(context.Background(), name)
}

func (fsys *FS) OpenContext(ctx context.Context, name string) (fs.File, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}

	// Synthetic root: "local" plus every recursive pin, so `ls /n/host/ipfs`
	// shows what this node actually holds.
	if name == "." {
		entries := []fs.DirEntry{fskit.Entry(localDir, fs.ModeDir|0755)}
		if cids, err := fsys.pins(ctx); err == nil {
			for _, cid := range cids {
				entries = append(entries, fskit.Entry(cid, fs.ModeDir|0555))
			}
		}
		return fskit.DirFile(fskit.Entry(".", fs.ModeDir|0555), entries...), nil
	}

	if name == localDir {
		cids, err := fsys.pins(ctx)
		if err != nil {
			return nil, &fs.PathError{Op: "open", Path: name, Err: err}
		}
		entries := make([]fs.DirEntry, 0, len(cids))
		for _, cid := range cids {
			entries = append(entries, fskit.Entry(cid, 0444))
		}
		return fskit.DirFile(fskit.Entry(localDir, fs.ModeDir|0755), entries...), nil
	}

	// Anything under local/ is addressed by CID directly.
	ipfsPath := strings.TrimPrefix(name, localDir+"/")

	st, err := fsys.statPath(ctx, ipfsPath)
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: name, Err: err}
	}

	if st.isDir() {
		links, err := fsys.list(ctx, ipfsPath)
		if err != nil {
			return nil, &fs.PathError{Op: "open", Path: name, Err: err}
		}
		entries := make([]fs.DirEntry, 0, len(links))
		for _, l := range links {
			entries = append(entries, fskit.Entry(l.Name, entryMode(l.Type), l.Size))
		}
		return fskit.DirFile(fskit.Entry(path.Base(name), fs.ModeDir|0555), entries...), nil
	}

	data, err := fsys.cat(ctx, ipfsPath)
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: name, Err: err}
	}
	// RawNode.OpenContext only accepts "." -- it opens the node itself, not
	// a child -- so pass "." rather than the basename.
	return fskit.RawNode(path.Base(name), data, 0444).OpenContext(ctx, ".")
}
