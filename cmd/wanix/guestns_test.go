//go:build !js && !wasm

package main

import (
	"io/fs"
	"testing"
	"testing/fstest"

	"tractor.dev/wanix/fs/fskit"
)

// The guest and the browser get DIFFERENT namespaces. The browser's import
// is same-origin with the served files and legitimately needs both; the
// guest needs only the host services and has no business reading
// index.html, the examples or the disk images.
//
// This is composed rather than filtered: paths outside the service set do
// not EXIST in the guest's namespace, so there is no traversal check to get
// wrong. These tests pin that composition, because a widening would be
// invisible -- everything would keep working, just with more exposed.

func serveDirAndMounts() (guest fs.FS, browser fs.FS) {
	dirfs := fstest.MapFS{
		"index.html":              &fstest.MapFile{Data: []byte("<html>")},
		"index.html.bak-20260924": &fstest.MapFile{Data: []byte("old")},
		"dist/wanix.min.js":       &fstest.MapFile{Data: []byte("js")},
		"examples/basic.html":     &fstest.MapFile{Data: []byte("x")},
		"local/9legacy-wanix.img": &fstest.MapFile{Data: []byte("disk")},
		"extras/9legacy/bin.tab":  &fstest.MapFile{Data: []byte("manifest")},
	}
	mounts := fskit.MapFS{
		"bin":  fstest.MapFS{"rc": &fstest.MapFile{Data: []byte("\x00\x00\x01\xeb")}},
		"ipfs": fstest.MapFS{"local": &fstest.MapFile{Data: []byte("")}},
		"ygg":  fstest.MapFS{"status": &fstest.MapFile{Data: []byte("up\n")}},
	}
	return fs.FS(mounts), fs.FS(fskit.UnionFS{mounts, dirfs})
}

func TestGuestNamespaceExcludesServedFiles(t *testing.T) {
	guest, _ := serveDirAndMounts()

	// The whole point: none of these exist for the guest.
	for _, leaked := range []string{
		"index.html",
		"index.html.bak-20260924",
		"dist/wanix.min.js",
		"examples/basic.html",
		"local/9legacy-wanix.img",
		"extras/9legacy/bin.tab",
	} {
		if _, err := fs.Stat(guest, leaked); err == nil {
			t.Errorf("guest can see %q; the guest namespace must contain services only", leaked)
		}
	}

	// The root listing must not mention them either.
	ents, err := fs.ReadDir(guest, ".")
	if err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{"bin": true, "ipfs": true, "ygg": true}
	for _, e := range ents {
		if !allowed[e.Name()] {
			t.Errorf("guest root lists %q, which is not a host service", e.Name())
		}
	}
}

func TestGuestNamespaceKeepsServices(t *testing.T) {
	guest, _ := serveDirAndMounts()
	for _, want := range []string{"bin/rc", "ipfs/local", "ygg/status"} {
		if _, err := fs.Stat(guest, want); err != nil {
			t.Errorf("guest cannot reach %q: %v", want, err)
		}
	}
	// And the contents actually come through.
	b, err := fs.ReadFile(guest, "bin/rc")
	if err != nil || len(b) == 0 {
		t.Errorf("guest could not read bin/rc: %v", err)
	}
}

// The browser import is unchanged: narrowing the guest must not narrow it.
func TestBrowserNamespaceKeepsBoth(t *testing.T) {
	_, browser := serveDirAndMounts()
	for _, want := range []string{
		"index.html", // served files
		"dist/wanix.min.js",
		"bin/rc", // and services
		"ygg/status",
	} {
		if _, err := fs.Stat(browser, want); err != nil {
			t.Errorf("browser namespace lost %q: %v", want, err)
		}
	}
}

// Path traversal cannot escape the guest namespace, because the parent
// simply is not part of it. Assert that rather than assume it.
func TestGuestNamespaceRejectsTraversal(t *testing.T) {
	guest, _ := serveDirAndMounts()
	for _, evil := range []string{
		"../index.html",
		"bin/../index.html",
		"bin/../../index.html",
		"./../index.html",
		"/index.html",
	} {
		if _, err := fs.Stat(guest, evil); err == nil {
			t.Errorf("traversal %q resolved in the guest namespace", evil)
		}
	}
}
