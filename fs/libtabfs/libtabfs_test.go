package libtabfs

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"
)

// buildManifest signs each file the way extras/9legacy/build_libtab.py does:
// Ed25519 over the canonical "path=<p> size=<n> sha256=<hex>".
func buildManifest(t *testing.T, priv ed25519.PrivateKey, files map[string][]byte) []byte {
	t.Helper()
	var b strings.Builder
	b.WriteString("# libtab test manifest\nschema=binaries\n\n")
	for name, data := range files {
		sum := sha256.Sum256(data)
		body := fmt.Sprintf("path=%s size=%d sha256=%s",
			name, len(data), hex.EncodeToString(sum[:]))
		sig := ed25519.Sign(priv, []byte(body))
		fmt.Fprintf(&b, "path=%s\n\tsize=%d\n\tsha256=%s\n\tattest=signed:%s:%s\n\n",
			name, len(data), hex.EncodeToString(sum[:]),
			base64.URLEncoding.EncodeToString([]byte(body)),
			base64.URLEncoding.EncodeToString(sig))
	}
	return []byte(b.String())
}

func testFS(t *testing.T) (*FS, fstest.MapFS, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	contents := map[string][]byte{
		"rc":      []byte("#!/bin/rc\necho hello\n"),
		"ls":      []byte("\x00\x01binary ls\x00"),
		"ip/ping": []byte("ping binary"),
	}
	src := fstest.MapFS{}
	for name, data := range contents {
		src[name] = &fstest.MapFile{Data: data, Mode: 0755}
	}
	manifest := buildManifest(t, priv, contents)
	f, err := New(src, manifest, pub)
	if err != nil {
		t.Fatal(err)
	}
	return f, src, priv
}

func TestVerifiedFilesRead(t *testing.T) {
	f, _, _ := testFS(t)
	for _, name := range []string{"rc", "ls", "ip/ping"} {
		if _, err := fs.ReadFile(f, name); err != nil {
			t.Errorf("read %s: %v", name, err)
		}
		if !f.Verified(name) {
			t.Errorf("%s should be marked verified after a successful read", name)
		}
	}
}

// THE core property: a binary whose bytes do not match the signed manifest
// must not be readable. Under design C the guest binds this tree straight
// into /bin, so "returns an error" is the difference between a tampered
// binary being unusable and being executed.
func TestTamperedFileCannotBeRead(t *testing.T) {
	f, src, _ := testFS(t)

	// Flip the contents of ls after the manifest was signed.
	src["ls"] = &fstest.MapFile{Data: []byte("\x00\x01EVIL ls\x00\x00"), Mode: 0755}

	_, err := fs.ReadFile(f, "ls")
	if err == nil {
		t.Fatal("tampered binary was readable; it must not be")
	}
	if !errors.Is(err, ErrUnverified) {
		t.Errorf("want ErrUnverified, got %v", err)
	}
	if f.Verified("ls") {
		t.Error("tampered binary must not be marked verified")
	}

	// Open must fail too, not just ReadFile -- exec goes through Open.
	if _, err := f.Open("ls"); err == nil {
		t.Fatal("Open succeeded on a tampered binary")
	}
}

// Same size, different bytes: catches a check that only compares length.
func TestSameSizeTamperIsCaught(t *testing.T) {
	f, src, _ := testFS(t)
	orig := src["rc"].Data
	evil := make([]byte, len(orig))
	copy(evil, orig)
	evil[len(evil)-2] ^= 0xff
	src["rc"] = &fstest.MapFile{Data: evil, Mode: 0755}

	_, err := fs.ReadFile(f, "rc")
	if err == nil {
		t.Fatal("same-size tamper was readable")
	}
	if !errors.Is(err, ErrUnverified) {
		t.Errorf("want ErrUnverified, got %v", err)
	}
}

// A file present on disk but absent from the manifest must not be served:
// otherwise dropping a binary into the directory bypasses signing entirely.
func TestUnlistedFileIsNotServed(t *testing.T) {
	f, src, _ := testFS(t)
	src["backdoor"] = &fstest.MapFile{Data: []byte("evil"), Mode: 0755}

	if _, err := f.Open("backdoor"); err == nil {
		t.Fatal("a file absent from the manifest must not be served")
	}
	for _, e := range f.dirEntries(".") {
		if e.Name() == "backdoor" {
			t.Fatal("unlisted file appeared in the directory listing")
		}
	}
}

// A manifest signed by a different key must be rejected wholesale.
func TestWrongSigningKeyRejected(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(nil)
	otherPub, _, _ := ed25519.GenerateKey(nil)
	contents := map[string][]byte{"rc": []byte("hi")}
	src := fstest.MapFS{"rc": &fstest.MapFile{Data: contents["rc"]}}
	manifest := buildManifest(t, priv, contents)

	if _, err := New(src, manifest, otherPub); err == nil {
		t.Fatal("manifest signed by an unknown key was accepted")
	}
}

// A manifest whose visible columns disagree with the signed body must be
// rejected: otherwise an attacker edits size/sha256 in the plaintext and
// the signature still checks out over the original body.
func TestManifestBodyMismatchRejected(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	data := []byte("real")
	sum := sha256.Sum256(data)
	body := fmt.Sprintf("path=rc size=%d sha256=%s", len(data), hex.EncodeToString(sum[:]))
	sig := ed25519.Sign(priv, []byte(body))

	evil := sha256.Sum256([]byte("fake"))
	manifest := fmt.Sprintf(
		"schema=binaries\n\npath=rc\n\tsize=%d\n\tsha256=%s\n\tattest=signed:%s:%s\n",
		len(data), hex.EncodeToString(evil[:]),
		base64.URLEncoding.EncodeToString([]byte(body)),
		base64.URLEncoding.EncodeToString(sig))

	src := fstest.MapFS{"rc": &fstest.MapFile{Data: data}}
	if _, err := New(src, []byte(manifest), pub); err == nil {
		t.Fatal("manifest whose columns disagree with the signed body was accepted")
	}
}

func TestCorruptSignatureRejected(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	contents := map[string][]byte{"rc": []byte("hi")}
	manifest := string(buildManifest(t, priv, contents))
	// Corrupt the signature's base64 payload.
	manifest = strings.Replace(manifest, "attest=signed:", "attest=signed:AAAA", 1)
	src := fstest.MapFS{"rc": &fstest.MapFile{Data: contents["rc"]}}

	if _, err := New(src, []byte(manifest), pub); err == nil {
		t.Fatal("corrupt attestation was accepted")
	}
}

func TestEmptyManifestRejected(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(nil)
	if _, err := New(fstest.MapFS{}, []byte("# nothing\n"), pub); err == nil {
		t.Fatal("empty manifest was accepted")
	}
}

func TestBadPublicKeySizeRejected(t *testing.T) {
	if _, err := New(fstest.MapFS{}, []byte("x"), ed25519.PublicKey([]byte{1, 2, 3})); err == nil {
		t.Fatal("short public key was accepted")
	}
}

// Stat must answer from the manifest, not by hashing: `ls` of ~800 binaries
// would otherwise hash the whole 114 MB tree.
func TestStatDoesNotVerifyContents(t *testing.T) {
	f, src, _ := testFS(t)
	src["ls"] = &fstest.MapFile{Data: []byte("tampered"), Mode: 0755}

	st, err := f.Stat("ls")
	if err != nil {
		t.Fatalf("stat should succeed from the manifest: %v", err)
	}
	// Reports the SIGNED size, not what is on disk.
	if st.Size() != int64(len(src["rc"].Data))-0 && st.Size() == int64(len("tampered")) {
		t.Error("stat reported the on-disk size of a tampered file")
	}
	if f.Verified("ls") {
		t.Error("stat must not mark a file verified")
	}
	// ...but reading it still fails.
	if _, err := fs.ReadFile(f, "ls"); !errors.Is(err, ErrUnverified) {
		t.Errorf("read after stat: want ErrUnverified, got %v", err)
	}
}

func TestDirectoriesAreSynthesised(t *testing.T) {
	f, _, _ := testFS(t)
	ents, err := fs.ReadDir(f, ".")
	if err != nil {
		t.Fatal(err)
	}
	var sawIPDir bool
	for _, e := range ents {
		if e.Name() == "ip" {
			sawIPDir = true
			if !e.IsDir() {
				t.Error("ip should be a directory")
			}
		}
	}
	if !sawIPDir {
		t.Error("synthesised directory ip/ missing from root listing")
	}

	sub, err := fs.ReadDir(f, "ip")
	if err != nil {
		t.Fatal(err)
	}
	if len(sub) != 1 || sub[0].Name() != "ping" {
		t.Errorf("ip/ listing wrong: %v", sub)
	}
}

func TestMissingFileIsNotExist(t *testing.T) {
	f, _, _ := testFS(t)
	_, err := f.Open("nope")
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("want ErrNotExist, got %v", err)
	}
	// A tampered file must NOT look like a missing one.
	if errors.Is(err, ErrUnverified) {
		t.Error("missing file must not report as unverified")
	}
}

// A manifest entry whose file is absent must fail to open rather than
// serving empty content.
func TestManifestEntryWithNoFile(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	contents := map[string][]byte{"ghost": []byte("gone")}
	manifest := buildManifest(t, priv, contents)
	f, err := New(fstest.MapFS{}, manifest, pub)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Open("ghost"); err == nil {
		t.Fatal("entry with no backing file should not open")
	}
}
