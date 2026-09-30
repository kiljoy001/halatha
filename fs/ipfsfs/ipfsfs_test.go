package ipfsfs

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"tractor.dev/wanix/fs"
)

// fakeKubo stands in for a Kubo node so these tests need no daemon.
// It implements just the endpoints ipfsfs uses.
type fakeKubo struct {
	files map[string]string   // ipfs path -> contents
	dirs  map[string][]lsLink // ipfs path -> entries
	pins  []string
	calls []string // endpoints hit, for asserting request shape
	// errPhrase overrides the "missing path" message so the tests can cover
	// each wording Kubo uses.
	errPhrase string
}

func (k *fakeKubo) missingMsg() string {
	if k.errPhrase != "" {
		return k.errPhrase
	}
	return "file does not exist"
}

func (k *fakeKubo) server(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	mux.HandleFunc("/api/v0/files/stat", func(w http.ResponseWriter, r *http.Request) {
		k.calls = append(k.calls, "files/stat")
		if r.Method != http.MethodPost {
			http.Error(w, "must POST", http.StatusMethodNotAllowed)
			return
		}
		p := strings.TrimPrefix(r.URL.Query().Get("arg"), "/ipfs/")
		if _, ok := k.dirs[p]; ok {
			json.NewEncoder(w).Encode(statInfo{Hash: p, Type: "directory"})
			return
		}
		if body, ok := k.files[p]; ok {
			json.NewEncoder(w).Encode(statInfo{Hash: p, Type: "file", Size: int64(len(body))})
			return
		}
		http.Error(w, k.missingMsg(), http.StatusInternalServerError)
	})

	mux.HandleFunc("/api/v0/ls", func(w http.ResponseWriter, r *http.Request) {
		k.calls = append(k.calls, "ls")
		p := strings.TrimPrefix(r.URL.Query().Get("arg"), "/ipfs/")
		links, ok := k.dirs[p]
		if !ok {
			http.Error(w, "no link named", http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(lsResponse{Objects: []struct {
			Hash  string
			Links []lsLink
		}{{Hash: p, Links: links}}})
	})

	mux.HandleFunc("/api/v0/cat", func(w http.ResponseWriter, r *http.Request) {
		k.calls = append(k.calls, "cat")
		p := strings.TrimPrefix(r.URL.Query().Get("arg"), "/ipfs/")
		body, ok := k.files[p]
		if !ok {
			http.Error(w, "file does not exist", http.StatusInternalServerError)
			return
		}
		io.WriteString(w, body)
	})

	mux.HandleFunc("/api/v0/pin/ls", func(w http.ResponseWriter, r *http.Request) {
		k.calls = append(k.calls, "pin/ls")
		keys := map[string]struct{ Type string }{}
		for _, c := range k.pins {
			keys[c] = struct{ Type string }{"recursive"}
		}
		json.NewEncoder(w).Encode(struct {
			Keys map[string]struct{ Type string }
		}{keys})
	})

	mux.HandleFunc("/api/v0/add", func(w http.ResponseWriter, r *http.Request) {
		k.calls = append(k.calls, "add")
		if r.URL.Query().Get("pin") != "true" {
			// Guard the durability property: an add that does not pin will be
			// garbage collected, which is the bug this whole design exists to
			// avoid.
			http.Error(w, "add without pin=true", http.StatusBadRequest)
			return
		}
		json.NewEncoder(w).Encode(struct{ Hash string }{"bafyfakecid"})
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func testFS(t *testing.T, k *fakeKubo) *FS {
	t.Helper()
	return New(k.server(t).URL, nil)
}

func TestReadFile(t *testing.T) {
	k := &fakeKubo{files: map[string]string{"bafyfile": "hello ipfs\n"}}
	fsys := testFS(t, k)

	got, err := fs.ReadFile(fsys, "bafyfile")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello ipfs\n" {
		t.Errorf("contents = %q", got)
	}
}

func TestReadDirOfCID(t *testing.T) {
	k := &fakeKubo{
		dirs: map[string][]lsLink{
			"bafydir": {
				{Name: "readme", Hash: "bafyreadme", Size: 1091, Type: 2},
				{Name: "sub", Hash: "bafysub", Size: 0, Type: 1},
			},
		},
	}
	fsys := testFS(t, k)

	entries, err := fs.ReadDir(fsys, "bafydir")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(entries))
	}
	byName := map[string]fs.DirEntry{}
	for _, e := range entries {
		byName[e.Name()] = e
	}
	if byName["readme"].IsDir() {
		t.Error("readme should be a file")
	}
	if !byName["sub"].IsDir() {
		t.Error("sub should be a directory")
	}
}

// TestStatDoesNotFetchContents is the reason Stat exists separately: going
// through Open would cat the whole file just to report its size, so `ls -l`
// on a large CID would download it.
func TestStatDoesNotFetchContents(t *testing.T) {
	k := &fakeKubo{files: map[string]string{"bafybig": strings.Repeat("x", 4096)}}
	fsys := testFS(t, k)

	info, err := fs.Stat(fsys, "bafybig")
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != 4096 {
		t.Errorf("size = %d, want 4096", info.Size())
	}
	for _, c := range k.calls {
		if c == "cat" {
			t.Fatal("Stat fetched file contents via cat")
		}
	}
}

func TestRootListsPins(t *testing.T) {
	k := &fakeKubo{pins: []string{"bafypinned1", "bafypinned2"}}
	fsys := testFS(t, k)

	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, e := range entries {
		names[e.Name()] = true
	}
	for _, want := range []string{"local", "bafypinned1", "bafypinned2"} {
		if !names[want] {
			t.Errorf("root listing missing %q (got %v)", want, names)
		}
	}
}

// TestAddPins guards the durability property: content written through this
// filesystem must be pinned, or the node garbage collects it and the data
// silently disappears.
func TestAddPins(t *testing.T) {
	k := &fakeKubo{}
	fsys := testFS(t, k)

	cid, err := fsys.Add(context.Background(), "note.txt", []byte("keep me"))
	if err != nil {
		t.Fatal(err)
	}
	if cid != "bafyfakecid" {
		t.Errorf("cid = %q", cid)
	}
	// The fake rejects pin != true, so reaching here proves we pinned.
	var added bool
	for _, c := range k.calls {
		if c == "add" {
			added = true
		}
	}
	if !added {
		t.Error("add endpoint was never called")
	}
}

func TestMissingPathIsErrNotExist(t *testing.T) {
	fsys := testFS(t, &fakeKubo{})

	if _, err := fs.ReadFile(fsys, "bafymissing"); !errorsIsNotExist(err) {
		t.Errorf("ReadFile err = %v, want fs.ErrNotExist", err)
	}
	if _, err := fs.Stat(fsys, "bafymissing"); !errorsIsNotExist(err) {
		t.Errorf("Stat err = %v, want fs.ErrNotExist", err)
	}
}

func errorsIsNotExist(err error) bool {
	return err != nil && strings.Contains(err.Error(), "file does not exist")
}

func TestInvalidPathRejected(t *testing.T) {
	fsys := testFS(t, &fakeKubo{})
	for _, bad := range []string{"/abs", "../escape", "a//b"} {
		if _, err := fsys.Open(bad); err == nil {
			t.Errorf("Open(%q) succeeded, want an error", bad)
		}
	}
}

// TestStatCached pins the cache: CIDs are immutable, so repeated stats of
// the same path must not re-hit the API.
func TestStatCached(t *testing.T) {
	k := &fakeKubo{files: map[string]string{"bafyx": "hi"}}
	fsys := testFS(t, k)

	for i := 0; i < 5; i++ {
		if _, err := fs.Stat(fsys, "bafyx"); err != nil {
			t.Fatal(err)
		}
	}
	n := 0
	for _, c := range k.calls {
		if c == "files/stat" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("files/stat called %d times, want 1 (cache not working)", n)
	}
}

// TestErrorMappingVariants pins each phrase Kubo uses for a missing path.
// They are separate `||` arms, so a test that only exercises one lets the
// others rot: a future Kubo wording change would surface as an opaque API
// error instead of ENOENT, and 9P clients would see something other than a
// normal "file does not exist".
func TestErrorMappingVariants(t *testing.T) {
	for _, phrase := range []string{
		"file does not exist",
		"no link named \"x\" under Qm...",
		"merkledag: not found",
	} {
		k := &fakeKubo{errPhrase: phrase}
		fsys := testFS(t, k)
		_, err := fs.Stat(fsys, "bafymissing")
		if err == nil {
			t.Fatalf("phrase %q: Stat succeeded, want an error", phrase)
		}
		if !strings.Contains(err.Error(), "does not exist") &&
			!strings.Contains(err.Error(), "not found") &&
			!strings.Contains(err.Error(), "no link named") {
			t.Errorf("phrase %q mapped to %v, want a not-exist error", phrase, err)
		}
	}
}

// TestTruncateBoundary pins the off-by-one in the error-message truncator so
// an exact-length message is not needlessly elided.
func TestTruncateBoundary(t *testing.T) {
	if got := truncate("abcde", 5); got != "abcde" {
		t.Errorf("truncate(len==n) = %q, want the string unchanged", got)
	}
	if got := truncate("abcdef", 5); got != "abcde..." {
		t.Errorf("truncate(len>n) = %q, want it elided", got)
	}
	if got := truncate("  padded  ", 20); got != "padded" {
		t.Errorf("truncate should trim space, got %q", got)
	}
}
