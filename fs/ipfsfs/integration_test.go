package ipfsfs

import (
	"context"
	"os"
	"strings"
	"testing"

	"tractor.dev/wanix/fs"
)

// These run against a real Kubo node. Point IPFS_API at one to enable them:
//
//	IPFS_API=http://127.0.0.1:5001 go test ./fs/ipfsfs/ -run Integration -v
//
// Over an SSH tunnel to the VPS:
//
//	ssh -N -L 5001:127.0.0.1:5001 root@halatha.rentonsoftworks.com
func integrationFS(t *testing.T) *FS {
	t.Helper()
	api := os.Getenv("IPFS_API")
	if api == "" {
		t.Skip("set IPFS_API to run against a real Kubo node")
	}
	return New(api, nil)
}

// The IPFS docs directory: a widely pinned, stable directory CID.
const docsCID = "QmYwAPJzv5CZsnA625s3Xf2nemtYgPpHdWEz79ojWnPbdG"

func TestIntegrationReadDir(t *testing.T) {
	fsys := integrationFS(t)
	entries, err := fs.ReadDir(fsys, docsCID)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("no entries")
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	t.Logf("entries: %v", names)
	found := false
	for _, n := range names {
		if n == "readme" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a readme entry, got %v", names)
	}
}

func TestIntegrationReadFile(t *testing.T) {
	fsys := integrationFS(t)
	data, err := fs.ReadFile(fsys, docsCID+"/readme")
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 {
		t.Fatal("empty file")
	}
	t.Logf("readme (%d bytes): %.60q...", len(data), string(data))
}

func TestIntegrationStat(t *testing.T) {
	fsys := integrationFS(t)

	di, err := fs.Stat(fsys, docsCID)
	if err != nil {
		t.Fatal(err)
	}
	if !di.IsDir() {
		t.Error("directory CID did not stat as a directory")
	}

	fi, err := fs.Stat(fsys, docsCID+"/readme")
	if err != nil {
		t.Fatal(err)
	}
	if fi.IsDir() {
		t.Error("readme stat'd as a directory")
	}
	if fi.Size() <= 0 {
		t.Errorf("readme size = %d", fi.Size())
	}
	t.Logf("readme size %d", fi.Size())
}

// TestIntegrationAddReadBack is the durability round trip: publish, pin,
// then read the content back by its CID.
func TestIntegrationAddReadBack(t *testing.T) {
	fsys := integrationFS(t)
	ctx := context.Background()

	payload := "halatha ipfsfs integration " + t.Name()
	cid, err := fsys.Add(ctx, "test.txt", []byte(payload))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("added CID %s", cid)

	got, err := fs.ReadFile(fsys, cid)
	if err != nil {
		t.Fatalf("reading back %s: %v", cid, err)
	}
	if string(got) != payload {
		t.Errorf("round trip mismatch:\n got %q\nwant %q", got, payload)
	}

	// It must be pinned, or the node will garbage collect it.
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		t.Fatal(err)
	}
	pinned := false
	for _, e := range entries {
		if e.Name() == cid {
			pinned = true
		}
	}
	if !pinned {
		t.Errorf("CID %s is not in the pin list after Add", cid)
	}

	t.Cleanup(func() {
		if err := fsys.Unpin(ctx, cid); err != nil {
			t.Logf("unpin %s: %v", cid, err)
		}
	})
}

func TestIntegrationWalkDir(t *testing.T) {
	fsys := integrationFS(t)
	var seen []string
	err := fs.WalkDir(fsys, docsCID, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		seen = append(seen, strings.TrimPrefix(p, docsCID+"/"))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("walked %d paths: %v", len(seen), seen)
	if len(seen) < 2 {
		t.Errorf("walk found %d paths, expected the directory plus children", len(seen))
	}
}
