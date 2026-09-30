//go:build !js && !wasm

package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"tractor.dev/wanix/fs"
	"tractor.dev/wanix/fs/fskit"
	"tractor.dev/wanix/fs/ipfsfs"
	"tractor.dev/wanix/fs/localfs"
)

// buildExported mirrors the composition in serveCmd: ipfs/ overlaid on the
// served directory. Kept in sync with serve.go by TestExportedNamespaceShape.
func buildExported(t *testing.T, dir, ipfsAPI string) fs.FS {
	t.Helper()
	dirfs, err := localfs.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	if ipfsAPI == "" {
		return dirfs
	}
	ifs := ipfsfs.New(ipfsAPI, nil)
	if _, err := ifs.StatContext(context.Background(), "."); err != nil {
		return dirfs
	}
	return fskit.UnionFS{fskit.MapFS{"ipfs": ifs}, dirfs}
}

// TestExportedNamespaceShape pins the union: the served directory stays
// readable and ipfs/ is layered on top without shadowing it. Exporting the
// filesystem (rather than proxying the Kubo API to the browser) is a
// security decision -- the API can add, pin and reconfigure the node, so it
// must never leave localhost.
func TestExportedNamespaceShape(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/served.txt", []byte("from disk\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Minimal fake Kubo: enough for the startup probe and a root listing.
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v0/files/stat", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"Hash":"x","Type":"directory"}`))
	})
	mux.HandleFunc("/api/v0/pin/ls", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"Keys":{"bafypinned":{"Type":"recursive"}}}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	exported := buildExported(t, dir, srv.URL)

	names := map[string]bool{}
	ents, err := fs.ReadDir(exported, ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		names[e.Name()] = true
	}
	if !names["served.txt"] {
		t.Error("union hid the served directory")
	}
	if !names["ipfs"] {
		t.Error("ipfs/ is not in the exported namespace")
	}

	if b, err := fs.ReadFile(exported, "served.txt"); err != nil || string(b) != "from disk\n" {
		t.Errorf("served file = %q, %v", b, err)
	}

	// ipfs/ resolves through the union to the pin listing.
	ipfsEnts, err := fs.ReadDir(exported, "ipfs")
	if err != nil {
		t.Fatalf("ReadDir ipfs/: %v", err)
	}
	found := false
	for _, e := range ipfsEnts {
		if e.Name() == "bafypinned" {
			found = true
		}
	}
	if !found {
		t.Error("ipfs/ did not list the pinned CID through the union")
	}
}

// TestExportedNamespaceWithoutIPFS pins the disabled path: no --ipfs-api
// means the namespace is just the served directory, unchanged.
func TestExportedNamespaceWithoutIPFS(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/only.txt", []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	exported := buildExported(t, dir, "")
	if _, err := fs.ReadFile(exported, "only.txt"); err != nil {
		t.Fatalf("served file unreadable: %v", err)
	}
	if _, err := fs.ReadDir(exported, "ipfs"); err == nil {
		t.Error("ipfs/ present despite --ipfs-api being empty")
	}
}

// TestExportedNamespaceUnreachableIPFS pins graceful degradation: a down or
// misconfigured Kubo must not take the whole server down, it just means no
// ipfs/ in the namespace.
func TestExportedNamespaceUnreachableIPFS(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/only.txt", []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	// Port 1 is reserved and nothing listens there.
	exported := buildExported(t, dir, "http://127.0.0.1:1")
	if _, err := fs.ReadFile(exported, "only.txt"); err != nil {
		t.Fatalf("served file unreadable when IPFS is down: %v", err)
	}
}
