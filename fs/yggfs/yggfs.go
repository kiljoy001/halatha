// Package yggfs exposes a host Yggdrasil daemon's admin API as a read-only
// fs.FS, so a guest can use the host's already-connected mesh node instead
// of running its own.
//
// Why this exists: the guest's yggdrasil (Plan 9 386) has been unreliable,
// and the guest's only TCP egress is the WISP relay, which deliberately
// blocks loopback, RFC 1918 and the VPS's own Yggdrasil address as an SSRF
// defense (cmd/wanix/serve.go). Rather than punch a hole in that filter,
// this reads the admin socket host-side and publishes the result into the
// existing 9P export. The guest gets a mesh view with no new egress path,
// no new listener, and no guest-side configuration.
//
// The admin API is line-free JSON over a unix socket: write one request
// object, half-close, read one response object. Endpoints and field names
// were taken from a live 0.5.14 daemon rather than documentation.
package yggfs

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"tractor.dev/wanix/fs/fskit"
)

// DefaultSocket is where the Debian/Ubuntu yggdrasil package puts its admin
// socket. Note this is NOT the upstream default (/var/run/yggdrasil.sock);
// yggdrasilctl itself fails over to the wrong path on these hosts.
const DefaultSocket = "/run/yggdrasil/yggdrasil.sock"

// cacheTTL bounds how stale a view can be. The admin socket is cheap, but a
// directory walk over 9P can issue many stats in a burst and there is no
// reason to re-query per entry. Peers and sessions change on the order of
// seconds, so a short TTL keeps `cat` honest without hammering the daemon.
const cacheTTL = 3 * time.Second

// FS is a read-only view of a Yggdrasil daemon.
//
// Layout:
//
//	address        the node's own Yggdrasil IPv6 address, one line
//	subnet         the node's /64, one line
//	key            public key, one line
//	version        build name and version, one line
//	peers          one peer per line: state, uri, address, rtt
//	sessions       one session per line: address, key, uptime
//	status         human summary; the one file to cat when something is wrong
//
// Every file ends in a newline so Plan 9 tools behave.
type FS struct {
	socket string
	// dialTimeout bounds a single admin round trip. A wedged daemon must
	// not be able to hang a 9P read, which would hang the guest's file
	// system rather than just this file.
	dialTimeout time.Duration

	mu    sync.Mutex
	cache map[string]*cacheEntry
}

type cacheEntry struct {
	data []byte
	at   time.Time
	err  error
}

// New returns an FS reading the daemon at socketPath (DefaultSocket if empty).
func New(socketPath string) *FS {
	if socketPath == "" {
		socketPath = DefaultSocket
	}
	return &FS{
		socket:      socketPath,
		dialTimeout: 5 * time.Second,
		cache:       make(map[string]*cacheEntry),
	}
}

// adminResponse is the envelope every admin reply uses.
type adminResponse struct {
	Status   string          `json:"status"`
	Error    string          `json:"error"`
	Response json.RawMessage `json:"response"`
}

// query performs one admin request. The protocol is: connect, write the
// request object, half-close so the daemon sees EOF, then read to EOF.
// Without the half-close the daemon waits for more input and the read
// blocks until the deadline.
func (f *FS) query(ctx context.Context, request string) (json.RawMessage, error) {
	d := net.Dialer{Timeout: f.dialTimeout}
	conn, err := d.DialContext(ctx, "unix", f.socket)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	deadline := time.Now().Add(f.dialTimeout)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	_ = conn.SetDeadline(deadline)

	body, err := json.Marshal(map[string]any{"request": request})
	if err != nil {
		return nil, err
	}
	if _, err := conn.Write(body); err != nil {
		return nil, err
	}
	// Half-close: the daemon reads until EOF before replying.
	if cw, ok := conn.(interface{ CloseWrite() error }); ok {
		if err := cw.CloseWrite(); err != nil {
			return nil, err
		}
	}

	var resp adminResponse
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return nil, err
	}
	if resp.Status != "success" {
		if resp.Error != "" {
			return nil, fmt.Errorf("yggdrasil: %s: %s", request, resp.Error)
		}
		return nil, fmt.Errorf("yggdrasil: %s: status %q", request, resp.Status)
	}
	return resp.Response, nil
}

type selfInfo struct {
	BuildName      string `json:"build_name"`
	BuildVersion   string `json:"build_version"`
	Key            string `json:"key"`
	Address        string `json:"address"`
	Subnet         string `json:"subnet"`
	RoutingEntries int    `json:"routing_entries"`
}

type peerInfo struct {
	Remote     string  `json:"remote"`
	Up         bool    `json:"up"`
	Inbound    bool    `json:"inbound"`
	Address    string  `json:"address"`
	Key        string  `json:"key"`
	Cost       uint64  `json:"cost"`
	BytesRecvd uint64  `json:"bytes_recvd"`
	BytesSent  uint64  `json:"bytes_sent"`
	Uptime     float64 `json:"uptime"`
	LastError  string  `json:"last_error"`
}

type sessionInfo struct {
	Address    string  `json:"address"`
	Key        string  `json:"key"`
	BytesRecvd uint64  `json:"bytes_recvd"`
	BytesSent  uint64  `json:"bytes_sent"`
	Uptime     float64 `json:"uptime"`
}

func (f *FS) self(ctx context.Context) (*selfInfo, error) {
	raw, err := f.query(ctx, "getself")
	if err != nil {
		return nil, err
	}
	var s selfInfo
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

func (f *FS) peers(ctx context.Context) ([]peerInfo, error) {
	raw, err := f.query(ctx, "getpeers")
	if err != nil {
		return nil, err
	}
	var wrap struct {
		Peers []peerInfo `json:"peers"`
	}
	if err := json.Unmarshal(raw, &wrap); err != nil {
		return nil, err
	}
	return wrap.Peers, nil
}

func (f *FS) sessions(ctx context.Context) ([]sessionInfo, error) {
	raw, err := f.query(ctx, "getsessions")
	if err != nil {
		return nil, err
	}
	var wrap struct {
		Sessions []sessionInfo `json:"sessions"`
	}
	if err := json.Unmarshal(raw, &wrap); err != nil {
		return nil, err
	}
	return wrap.Sessions, nil
}

func dur(seconds float64) string {
	d := time.Duration(seconds) * time.Second
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("%dd%dh", int(d.Hours())/24, int(d.Hours())%24)
}

// render builds the contents of one file. Errors become file contents
// rather than open failures: a guest running `cat status` when the daemon
// is down should see why, not get a bare error from the 9P layer.
func (f *FS) render(ctx context.Context, name string) ([]byte, error) {
	switch name {
	case "address":
		s, err := f.self(ctx)
		if err != nil {
			return nil, err
		}
		return []byte(s.Address + "\n"), nil

	case "subnet":
		s, err := f.self(ctx)
		if err != nil {
			return nil, err
		}
		return []byte(s.Subnet + "\n"), nil

	case "key":
		s, err := f.self(ctx)
		if err != nil {
			return nil, err
		}
		return []byte(s.Key + "\n"), nil

	case "version":
		s, err := f.self(ctx)
		if err != nil {
			return nil, err
		}
		return []byte(fmt.Sprintf("%s %s\n", s.BuildName, s.BuildVersion)), nil

	case "peers":
		ps, err := f.peers(ctx)
		if err != nil {
			return nil, err
		}
		// Sort up-first then by uri so the file is stable across reads and
		// the useful lines are at the top.
		sort.SliceStable(ps, func(i, j int) bool {
			if ps[i].Up != ps[j].Up {
				return ps[i].Up
			}
			return ps[i].Remote < ps[j].Remote
		})
		var b strings.Builder
		for _, p := range ps {
			state := "down"
			if p.Up {
				state = "up"
			}
			fmt.Fprintf(&b, "%-4s %-34s %-40s", state, p.Remote, p.Address)
			if p.Up {
				fmt.Fprintf(&b, " %8s %10s", dur(p.Uptime), "cost="+fmt.Sprint(p.Cost))
			} else if p.LastError != "" {
				fmt.Fprintf(&b, " %s", p.LastError)
			}
			b.WriteString("\n")
		}
		return []byte(b.String()), nil

	case "sessions":
		ss, err := f.sessions(ctx)
		if err != nil {
			return nil, err
		}
		sort.SliceStable(ss, func(i, j int) bool { return ss[i].Address < ss[j].Address })
		var b strings.Builder
		for _, s := range ss {
			fmt.Fprintf(&b, "%-40s %8s %s\n", s.Address, dur(s.Uptime), s.Key)
		}
		return []byte(b.String()), nil

	case "status":
		var b strings.Builder
		s, err := f.self(ctx)
		if err != nil {
			fmt.Fprintf(&b, "yggdrasil: UNREACHABLE via %s\n", f.socket)
			fmt.Fprintf(&b, "error: %v\n", err)
			return []byte(b.String()), nil
		}
		fmt.Fprintf(&b, "yggdrasil %s\n", s.BuildVersion)
		fmt.Fprintf(&b, "address %s\n", s.Address)
		fmt.Fprintf(&b, "subnet  %s\n", s.Subnet)
		fmt.Fprintf(&b, "routes  %d\n", s.RoutingEntries)
		if ps, err := f.peers(ctx); err == nil {
			up := 0
			for _, p := range ps {
				if p.Up {
					up++
				}
			}
			fmt.Fprintf(&b, "peers   %d up / %d total\n", up, len(ps))
		}
		if ss, err := f.sessions(ctx); err == nil {
			fmt.Fprintf(&b, "sessions %d\n", len(ss))
		}
		return []byte(b.String()), nil
	}
	return nil, fs.ErrNotExist
}

// content returns a file's bytes, memoised for cacheTTL.
func (f *FS) content(ctx context.Context, name string) ([]byte, error) {
	f.mu.Lock()
	if e, ok := f.cache[name]; ok && time.Since(e.at) < cacheTTL {
		f.mu.Unlock()
		return e.data, e.err
	}
	f.mu.Unlock()

	data, err := f.render(ctx, name)

	f.mu.Lock()
	f.cache[name] = &cacheEntry{data: data, at: time.Now(), err: err}
	f.mu.Unlock()
	return data, err
}

// files is the fixed set of names this FS publishes.
var files = []string{
	"address",
	"key",
	"peers",
	"sessions",
	"status",
	"subnet",
	"version",
}

func isFile(name string) bool {
	for _, f := range files {
		if f == name {
			return true
		}
	}
	return false
}

func (f *FS) Open(name string) (fs.File, error) {
	return f.OpenContext(context.Background(), name)
}

func (f *FS) OpenContext(ctx context.Context, name string) (fs.File, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}

	if name == "." {
		entries := make([]fs.DirEntry, 0, len(files))
		for _, n := range files {
			entries = append(entries, fskit.Entry(n, 0444))
		}
		return fskit.DirFile(fskit.Entry(".", fs.ModeDir|0555), entries...), nil
	}

	if !isFile(name) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}

	data, err := f.content(ctx, name)
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: name, Err: err}
	}
	// RawNode.OpenContext only accepts "." -- it opens the node itself,
	// not a child -- so pass "." rather than the basename.
	return fskit.RawNode(path.Base(name), data, 0444).OpenContext(ctx, ".")
}

// StatContext answers without querying the daemon. A directory walk stats
// every entry, and making that hit the admin socket once per file would
// turn `ls` into a burst of round trips. Size is reported as 0, which is
// normal for synthetic files and which Plan 9 tools handle: they read until
// EOF rather than trusting the stat size.
func (f *FS) StatContext(ctx context.Context, name string) (fs.FileInfo, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "stat", Path: name, Err: fs.ErrInvalid}
	}
	if name == "." {
		return fskit.Entry(".", fs.ModeDir|0555).Info()
	}
	if !isFile(name) {
		return nil, &fs.PathError{Op: "stat", Path: name, Err: fs.ErrNotExist}
	}
	return fskit.Entry(path.Base(name), 0444).Info()
}

func (f *FS) Stat(name string) (fs.FileInfo, error) {
	return f.StatContext(context.Background(), name)
}
