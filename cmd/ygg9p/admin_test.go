package main

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Public test keys derived from fixed seeds (sha256 of "halatha test server
// key" and "halatha test transport key"); never a deployed identity.
const serverKey = "bb039e2dafc0669e85dcbb59cb4acc59f1960db693fd826743d98745e180b898"

func TestProvisionAndEnroll(t *testing.T) {
	// This public test vector is never installed in the deployed allowlist.
	id := "bdbacfd82240de3dcd123924cbb55256fb8dab08aa98e305528ab84f419e6efb"
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	data, _ := json.Marshal(configuration{ServerKey: serverKey})
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := load(path); err != nil {
		t.Fatal(err)
	}
	if err := allocateImage(path, id); err != nil {
		t.Fatal(err)
	}
	image := filepath.Join(dir, "images", id+".img")
	st, err := os.Stat(image)
	if err != nil || st.Size() != 1<<30 {
		t.Fatal("wrong image cap", err)
	}
	if err := allocateImage(path, id); err == nil {
		t.Fatal("existing image overwritten")
	}
	if err := enrollKey(path, id); err != nil {
		t.Fatal(err)
	}
	cfg, err := load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Exports[id].Access != "read-write" {
		t.Fatal("wrong access")
	}
	if err := enrollKey(path, id); err == nil {
		t.Fatal("duplicate enrolled")
	}
	backups, _ := filepath.Glob(path + ".backup-*")
	if len(backups) != 1 {
		t.Fatal("old allowlist not preserved")
	}
	f, err := os.OpenFile(image, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.Truncate(2 << 30)
	f.Close()
	if _, err := load(path); err == nil {
		t.Fatal("enlarged image accepted")
	}
}

func TestProvisionDelegatedClient(t *testing.T) {
	id := "bdbacfd82240de3dcd123924cbb55256fb8dab08aa98e305528ab84f419e6efb"
	transport := "430ba825e9812a0cb1fe6f3880373034d5fe1637067301d562b74288fa4e31d1"
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	b, _ := json.Marshal(configuration{ServerKey: serverKey})
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	if err := allocateImage(path, id); err != nil {
		t.Fatal(err)
	}
	if err := enrollKey(path, id, transport); err != nil {
		t.Fatal(err)
	}
	cfg, err := load(path)
	if err != nil {
		t.Fatal(err)
	}
	keys := cfg.Exports[id].TransportKeys
	if len(keys) != 1 {
		t.Fatal("missing transport allowlist")
	}
	if got := hex.EncodeToString(keys[0]); got != transport {
		t.Fatal(got)
	}
}

func TestRevokeRetainsDataAndSerializesAdministrators(t *testing.T) {
	id := "bdbacfd82240de3dcd123924cbb55256fb8dab08aa98e305528ab84f419e6efb"
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	b, _ := json.Marshal(configuration{ServerKey: serverKey})
	os.WriteFile(path, b, 0600)
	if err := allocateImage(path, id); err != nil {
		t.Fatal(err)
	}
	if err := enrollKey(path, id); err != nil {
		t.Fatal(err)
	}
	var records bytes.Buffer
	if err := listKeys(path, &records); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(records.String(), id+"\tdirect\tread-write\t1073741824\n") {
		t.Fatal(records.String())
	}
	unlock, err := lockConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := revokeKey(path, id); err == nil {
		t.Fatal("concurrent administrator bypassed lock")
	}
	cfg, err := load(path)
	if err != nil || len(cfg.Keys) != 1 {
		t.Fatal("locked update changed allowlist", err)
	}
	unlock()
	image := filepath.Join(dir, "images", id+".img")
	f, err := os.OpenFile(image, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteAt([]byte("retained-user-data"), 4096)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	if err := revokeKey(path, id); err != nil {
		t.Fatal(err)
	}
	cfg, err = load(path)
	if err != nil || len(cfg.Keys) != 0 {
		t.Fatal("revoked key remains", err)
	}
	f, err = os.Open(image)
	if err != nil {
		t.Fatal("revocation deleted image", err)
	}
	data := make([]byte, len("retained-user-data"))
	_, err = f.ReadAt(data, 4096)
	f.Close()
	if err != nil || string(data) != "retained-user-data" {
		t.Fatal("revocation changed stored data", err)
	}
	if err := revokeKey(path, id); err == nil {
		t.Fatal("unknown key revocation succeeded")
	}
	records.Reset()
	if err := listKeys(path, &records); err != nil || records.Len() != 0 {
		t.Fatal("list changed enrollment", err)
	}
}
