package yggfs

import (
	"encoding/json"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeDaemon serves the Yggdrasil admin protocol on a unix socket: read one
// JSON request to EOF, write one JSON response. Responses are keyed by
// request name so a test can script the daemon's answers.
type fakeDaemon struct {
	t     *testing.T
	path  string
	ln    net.Listener
	mu    sync.Mutex
	reply map[string]any
	// hang makes the daemon accept but never answer, to exercise timeouts.
	hang bool
	// calls counts requests per endpoint, to prove caching works.
	calls map[string]int
}

func newFakeDaemon(t *testing.T) *fakeDaemon {
	t.Helper()
	// Unix socket paths are capped near 108 bytes; t.TempDir() under a long
	// test name can exceed that, so keep the name short.
	dir, err := os.MkdirTemp("", "ygg")
	if err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(dir, "s")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	d := &fakeDaemon{t: t, path: sock, ln: ln, reply: map[string]any{}, calls: map[string]int{}}
	t.Cleanup(func() { ln.Close(); os.RemoveAll(dir) })
	go d.serve()
	return d
}

func (d *fakeDaemon) serve() {
	for {
		conn, err := d.ln.Accept()
		if err != nil {
			return
		}
		go func(c net.Conn) {
			defer c.Close()
			body, err := io.ReadAll(c)
			if err != nil {
				return
			}
			var req struct {
				Request string `json:"request"`
			}
			if err := json.Unmarshal(body, &req); err != nil {
				return
			}
			d.mu.Lock()
			d.calls[req.Request]++
			resp, ok := d.reply[req.Request]
			hang := d.hang
			d.mu.Unlock()

			if hang {
				select {}
			}
			out := map[string]any{"status": "success", "response": resp}
			if !ok {
				out = map[string]any{"status": "error", "error": "unknown request"}
			}
			_ = json.NewEncoder(c).Encode(out)
		}(conn)
	}
}

func (d *fakeDaemon) set(request string, response any) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.reply[request] = response
}

func (d *fakeDaemon) count(request string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls[request]
}

func healthyDaemon(t *testing.T) *fakeDaemon {
	d := newFakeDaemon(t)
	d.set("getself", map[string]any{
		"build_name": "yggdrasil", "build_version": "0.5.14",
		"key":     "48bf3da5",
		"address": "201:dd03:96a:1c97:7b9a:98ee:c9ab:1e60",
		"subnet":  "301:dd03:96a:1c97::/64", "routing_entries": 8,
	})
	d.set("getpeers", map[string]any{"peers": []any{
		map[string]any{"remote": "tcp://23.184.48.86:1443", "up": false, "last_error": "EOF"},
		map[string]any{"remote": "tls://leo.node.3dt.net:9003", "up": true,
			"address": "201:bbc5:f2e3:1847:8287:96d2:ef3c:4b3e", "uptime": 78195.0, "cost": 87},
	}})
	d.set("getsessions", map[string]any{"sessions": []any{
		map[string]any{"address": "200:f60e:ac7d:254a:ff09:254d:8d00:de8e",
			"key": "750b39ae", "uptime": 368999.0},
	}})
	return d
}

func readFile(t *testing.T, fsys fs.FS, name string) string {
	t.Helper()
	f, err := fsys.Open(name)
	if err != nil {
		t.Fatalf("open %s: %v", name, err)
	}
	defer f.Close()
	b, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}

func TestFilesReportDaemonState(t *testing.T) {
	d := healthyDaemon(t)
	fsys := New(d.path)

	cases := map[string]string{
		"address": "201:dd03:96a:1c97:7b9a:98ee:c9ab:1e60\n",
		"subnet":  "301:dd03:96a:1c97::/64\n",
		"key":     "48bf3da5\n",
		"version": "yggdrasil 0.5.14\n",
	}
	for name, want := range cases {
		if got := readFile(t, fsys, name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
}

// Every file must end in a newline: Plan 9 tools treat a file without one
// as a dropped final line, which is the single most recurrent footgun in
// this codebase.
func TestEveryFileEndsWithNewline(t *testing.T) {
	d := healthyDaemon(t)
	fsys := New(d.path)
	for _, name := range files {
		got := readFile(t, fsys, name)
		if got == "" {
			continue
		}
		if !strings.HasSuffix(got, "\n") {
			t.Errorf("%s does not end with a newline: %q", name, got)
		}
	}
}

func TestPeersListsUpFirst(t *testing.T) {
	d := healthyDaemon(t)
	got := readFile(t, New(d.path), "peers")
	lines := strings.Split(strings.TrimRight(got, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 peer lines, got %d: %q", len(lines), got)
	}
	if !strings.HasPrefix(lines[0], "up ") {
		t.Errorf("up peer must sort first, got %q", lines[0])
	}
	if !strings.HasPrefix(lines[1], "down") {
		t.Errorf("second line should be the down peer, got %q", lines[1])
	}
	// A down peer's reason is the actionable part; it must survive.
	if !strings.Contains(lines[1], "EOF") {
		t.Errorf("down peer must report last_error, got %q", lines[1])
	}
}

// status is the file a person cats when something is wrong, so it must
// still produce useful output when the daemon cannot be reached -- not an
// open error, which surfaces through 9P as an unhelpful failure.
func TestStatusReportsUnreachableDaemon(t *testing.T) {
	fsys := New("/nonexistent/ygg.sock")
	got := readFile(t, fsys, "status")
	if !strings.Contains(got, "UNREACHABLE") {
		t.Errorf("status should say UNREACHABLE, got %q", got)
	}
	if !strings.HasSuffix(got, "\n") {
		t.Error("status must end with a newline")
	}
}

// A file whose data genuinely cannot be produced should fail to open,
// rather than silently returning empty content that reads as "no peers".
func TestDataFilesFailWhenDaemonDown(t *testing.T) {
	fsys := New("/nonexistent/ygg.sock")
	for _, name := range []string{"address", "peers", "sessions"} {
		if _, err := fsys.Open(name); err == nil {
			t.Errorf("open %s should fail when the daemon is unreachable", name)
		}
	}
}

func TestRootListsAllFiles(t *testing.T) {
	d := healthyDaemon(t)
	f, err := New(d.path).Open(".")
	if err != nil {
		t.Fatalf("open .: %v", err)
	}
	defer f.Close()
	dir, ok := f.(fs.ReadDirFile)
	if !ok {
		t.Fatal("root is not a ReadDirFile")
	}
	entries, err := dir.ReadDir(-1)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != len(files) {
		t.Fatalf("root has %d entries, want %d", len(entries), len(files))
	}
	seen := map[string]bool{}
	for _, e := range entries {
		seen[e.Name()] = true
		if e.IsDir() {
			t.Errorf("%s should be a regular file", e.Name())
		}
	}
	for _, want := range files {
		if !seen[want] {
			t.Errorf("root listing missing %q", want)
		}
	}
}

func TestUnknownPathIsNotExist(t *testing.T) {
	d := healthyDaemon(t)
	fsys := New(d.path)
	for _, bad := range []string{"nope", "peers/extra", "addPeer"} {
		_, err := fsys.Open(bad)
		if err == nil {
			t.Errorf("open %q should fail", bad)
			continue
		}
		// Must be ErrNotExist so the 9P layer maps it correctly.
		if bad == "nope" && !isNotExist(err) {
			t.Errorf("open %q: want ErrNotExist, got %v", bad, err)
		}
	}
}

func isNotExist(err error) bool {
	var pe *fs.PathError
	if ok := asPathError(err, &pe); ok {
		return pe.Err == fs.ErrNotExist
	}
	return false
}

func asPathError(err error, out **fs.PathError) bool {
	pe, ok := err.(*fs.PathError)
	if ok {
		*out = pe
	}
	return ok
}

// Stat must not query the daemon. A directory walk stats every entry, and
// one admin round trip per file would make `ls` pathologically slow over
// a 9P link that is already the guest's bottleneck.
func TestStatDoesNotQueryDaemon(t *testing.T) {
	d := healthyDaemon(t)
	fsys := New(d.path)
	for _, name := range files {
		if _, err := fsys.Stat(name); err != nil {
			t.Fatalf("stat %s: %v", name, err)
		}
	}
	if n := d.count("getself"); n != 0 {
		t.Errorf("stat issued %d getself calls, want 0", n)
	}
}

// Stat must work even when the daemon is down, so `ls` of the directory
// does not fail just because yggdrasil is stopped.
func TestStatWorksWhenDaemonDown(t *testing.T) {
	fsys := New("/nonexistent/ygg.sock")
	for _, name := range files {
		if _, err := fsys.Stat(name); err != nil {
			t.Errorf("stat %s with daemon down: %v", name, err)
		}
	}
}

func TestContentIsCached(t *testing.T) {
	d := healthyDaemon(t)
	fsys := New(d.path)
	for i := 0; i < 5; i++ {
		readFile(t, fsys, "address")
	}
	if n := d.count("getself"); n != 1 {
		t.Errorf("5 reads issued %d getself calls, want 1 (cached)", n)
	}
}

func TestCacheExpires(t *testing.T) {
	d := healthyDaemon(t)
	fsys := New(d.path)
	readFile(t, fsys, "address")

	// Age the entry past the TTL rather than sleeping for it.
	fsys.mu.Lock()
	fsys.cache["address"].at = time.Now().Add(-cacheTTL - time.Second)
	fsys.mu.Unlock()

	readFile(t, fsys, "address")
	if n := d.count("getself"); n != 2 {
		t.Errorf("after TTL expiry got %d getself calls, want 2", n)
	}
}

// A wedged daemon must not be able to hang a 9P read indefinitely: that
// would hang the guest's whole filesystem, not just this file.
func TestHungDaemonTimesOut(t *testing.T) {
	d := newFakeDaemon(t)
	d.mu.Lock()
	d.hang = true
	d.mu.Unlock()

	fsys := New(d.path)
	fsys.dialTimeout = 300 * time.Millisecond

	done := make(chan error, 1)
	go func() {
		_, err := fsys.Open("address")
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Error("open should fail against a hung daemon")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("open hung on a wedged daemon; a 9P read would hang the guest")
	}
}

// The package must never expose a way to mutate the host's daemon. The
// guest is untrusted, and addPeer/removePeer would let a page reconfigure
// the host's mesh node.
func TestExposesNoMutatingEndpoints(t *testing.T) {
	// Exact names, not substrings: "address" legitimately contains "add".
	mutating := map[string]bool{
		"addpeer": true, "removepeer": true, "addpeers": true,
		"ctl": true, "config": true, "setconfig": true,
	}
	for _, name := range files {
		if mutating[strings.ToLower(name)] {
			t.Errorf("file %q is mutating; this FS must stay read-only", name)
		}
	}

	// The render switch must not reach any mutating admin endpoint either.
	// Reading the source is the only way to assert this, since a mutating
	// call would otherwise only show up against a live daemon.
	src, err := os.ReadFile("yggfs.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, verb := range []string{`"addpeer"`, `"removepeer"`, `"setconfig"`} {
		if strings.Contains(strings.ToLower(string(src)), verb) {
			t.Errorf("yggfs.go issues mutating admin request %s", verb)
		}
	}
}
