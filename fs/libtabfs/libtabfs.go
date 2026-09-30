// Package libtabfs serves a libtab-attested binary tree as an fs.FS, so a
// guest can mount it over 9P and execute from it directly instead of the
// browser fetching every file over HTTP and writing it into a ramfs.
//
// Why this exists: the 9legacy binary tree is ~114 MB across ~800 files.
// Pulling it through the browser cost one HTTP round trip per binary (~0.5s
// each, strictly sequential), so `pkg install all` could not finish inside
// any sane timeout and /n/host/bin held only the handful that had landed.
// The guest already speaks 9P, so serving the tree is both simpler and
// demand-paged: only the pages actually executed cross the wire.
//
// Trust: every manifest entry is individually Ed25519-signed over the
// canonical string "path=<p> size=<n> sha256=<hex>" (extras/9legacy/
// build_libtab.py). This package verifies that signature AND the file's
// SHA-256 before its contents are ever returned. A file that fails either
// check cannot be opened, so a corrupt or substituted binary is not
// executable rather than merely flagged -- the browser is no longer in the
// path, so this is where the guarantee has to live.
//
// Verification is per file and cached, so the cost is paid once per binary
// on first use, not once per tree on startup.
package libtabfs

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"

	"tractor.dev/wanix/fs/fskit"
)

// ErrUnverified is returned when a file's contents do not match the signed
// manifest. It is deliberately distinct from fs.ErrNotExist: a missing file
// and a tampered one are different problems and must not look alike.
var ErrUnverified = errors.New("libtab: verification failed")

// entry is one attested manifest record.
type entry struct {
	path   string
	size   int64
	sha256 []byte
	// body and sig are the exact bytes that were signed, kept verbatim so
	// verification never depends on re-deriving the canonical form.
	body []byte
	sig  []byte
}

// FS serves a verified view of a binary tree.
type FS struct {
	src    fs.FS
	pub    ed25519.PublicKey
	byPath map[string]*entry
	names  []string

	mu       sync.RWMutex
	verified map[string]bool
}

// New returns an FS serving src, admitting only files attested by manifest
// and signed by pub. The manifest's own entries are signature-checked here,
// at construction, so a malformed or unsigned manifest fails fast rather
// than at first read.
func New(src fs.FS, manifest []byte, pub ed25519.PublicKey) (*FS, error) {
	if len(pub) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("libtab: bad public key size %d", len(pub))
	}
	entries, err := parseManifest(manifest, pub)
	if err != nil {
		return nil, err
	}
	f := &FS{
		src:      src,
		pub:      pub,
		byPath:   entries,
		verified: make(map[string]bool),
	}
	for name := range entries {
		f.names = append(f.names, name)
	}
	sort.Strings(f.names)
	return f, nil
}

// NewFromFiles is the common case: read the manifest and a hex public key
// off disk.
func NewFromFiles(src fs.FS, manifestPath, pubHexPath string) (*FS, error) {
	manifest, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(pubHexPath)
	if err != nil {
		return nil, err
	}
	pub, err := hex.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil {
		return nil, fmt.Errorf("libtab: bad public key hex: %w", err)
	}
	return New(src, manifest, ed25519.PublicKey(pub))
}

// Count reports how many attested entries the manifest carries.
func (f *FS) Count() int { return len(f.byPath) }

// b64 is RFC 4648 §5 url-safe base64, which build_libtab.py emits. Padding
// is present in the manifest, so use the padded decoder.
func b64decode(s string) ([]byte, error) {
	return base64.URLEncoding.DecodeString(s)
}

// parseManifest reads the ndb-ish manifest and checks every signature.
func parseManifest(data []byte, pub ed25519.PublicKey) (map[string]*entry, error) {
	out := make(map[string]*entry)
	var cur *entry

	flush := func() error {
		if cur == nil {
			return nil
		}
		if cur.sig == nil || cur.body == nil {
			return fmt.Errorf("libtab: entry %q has no attestation", cur.path)
		}
		if !ed25519.Verify(pub, cur.body, cur.sig) {
			return fmt.Errorf("libtab: bad signature for %q", cur.path)
		}
		// The signed body is authoritative. Re-derive the fields from it
		// rather than trusting the plaintext columns, so a manifest whose
		// visible size/sha256 disagree with what was actually signed is
		// rejected instead of silently preferring the unsigned copy.
		want := fmt.Sprintf("path=%s size=%d sha256=%s",
			cur.path, cur.size, hex.EncodeToString(cur.sha256))
		if string(cur.body) != want {
			return fmt.Errorf("libtab: entry %q does not match its signed body", cur.path)
		}
		out[cur.path] = cur
		cur = nil
		return nil
	}

	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		key, val, ok := strings.Cut(trimmed, "=")
		if !ok {
			continue
		}
		switch key {
		case "path":
			if err := flush(); err != nil {
				return nil, err
			}
			cur = &entry{path: val}
		case "size":
			if cur == nil {
				continue
			}
			n, err := strconv.ParseInt(val, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("libtab: bad size for %q: %w", cur.path, err)
			}
			cur.size = n
		case "sha256":
			if cur == nil {
				continue
			}
			sum, err := hex.DecodeString(val)
			if err != nil || len(sum) != sha256.Size {
				return nil, fmt.Errorf("libtab: bad sha256 for %q", cur.path)
			}
			cur.sha256 = sum
		case "attest":
			if cur == nil {
				continue
			}
			// signed:<b64 body>:<b64 sig>
			rest, ok := strings.CutPrefix(val, "signed:")
			if !ok {
				return nil, fmt.Errorf("libtab: unsupported attestation for %q", cur.path)
			}
			bodyB64, sigB64, ok := strings.Cut(rest, ":")
			if !ok {
				return nil, fmt.Errorf("libtab: malformed attestation for %q", cur.path)
			}
			body, err := b64decode(bodyB64)
			if err != nil {
				return nil, fmt.Errorf("libtab: bad attestation body for %q: %w", cur.path, err)
			}
			sig, err := b64decode(sigB64)
			if err != nil {
				return nil, fmt.Errorf("libtab: bad attestation sig for %q: %w", cur.path, err)
			}
			cur.body, cur.sig = body, sig
		}
	}
	if err := flush(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, errors.New("libtab: manifest has no entries")
	}
	return out, nil
}

// read returns a file's bytes only if they match the signed manifest.
// This is the gate: a binary that fails here is never returned, so it can
// never be executed by the guest.
func (f *FS) read(ctx context.Context, e *entry) ([]byte, error) {
	data, err := fs.ReadFile(f.src, e.path)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) != e.size {
		return nil, fmt.Errorf("%w: %s: size %d, want %d",
			ErrUnverified, e.path, len(data), e.size)
	}
	sum := sha256.Sum256(data)
	if !bytes.Equal(sum[:], e.sha256) {
		return nil, fmt.Errorf("%w: %s: sha256 %s, want %s",
			ErrUnverified, e.path,
			hex.EncodeToString(sum[:]), hex.EncodeToString(e.sha256))
	}

	f.mu.Lock()
	f.verified[e.path] = true
	f.mu.Unlock()
	return data, nil
}

// Verified reports whether name has passed verification in this process.
func (f *FS) Verified(name string) bool {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.verified[name]
}

func (f *FS) Open(name string) (fs.File, error) {
	return f.OpenContext(context.Background(), name)
}

func (f *FS) OpenContext(ctx context.Context, name string) (fs.File, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}

	if name == "." {
		return fskit.DirFile(fskit.Entry(".", fs.ModeDir|0555), f.dirEntries(".")...), nil
	}

	if e, ok := f.byPath[name]; ok {
		data, err := f.read(ctx, e)
		if err != nil {
			return nil, &fs.PathError{Op: "open", Path: name, Err: err}
		}
		// RawNode.OpenContext only accepts "." -- it opens the node itself,
		// not a child -- so pass "." rather than the basename.
		return fskit.RawNode(path.Base(name), data, 0555).OpenContext(ctx, ".")
	}

	// A directory exists if any attested entry lives under it. The manifest
	// lists only files, so directories are synthesised.
	if ents := f.dirEntries(name); len(ents) > 0 {
		return fskit.DirFile(fskit.Entry(path.Base(name), fs.ModeDir|0555), ents...), nil
	}

	return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
}

// dirEntries lists the immediate children of dir, synthesising directories
// from the manifest's path prefixes.
func (f *FS) dirEntries(dir string) []fs.DirEntry {
	prefix := ""
	if dir != "." {
		prefix = dir + "/"
	}
	seenDir := make(map[string]bool)
	var out []fs.DirEntry
	for _, name := range f.names {
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		rest := name[len(prefix):]
		if rest == "" {
			continue
		}
		if i := strings.IndexByte(rest, '/'); i >= 0 {
			sub := rest[:i]
			if !seenDir[sub] {
				seenDir[sub] = true
				out = append(out, fskit.Entry(sub, fs.ModeDir|0555))
			}
			continue
		}
		out = append(out, fskit.Entry(rest, 0555, f.byPath[name].size))
	}
	return out
}

// StatContext answers from the manifest without reading or hashing the
// file. A directory walk stats every entry, and hashing ~800 binaries to
// answer `ls` would be pathological. Contents are still verified on open,
// so this never lets unverified bytes through -- it only reports what the
// signed manifest claims.
func (f *FS) StatContext(ctx context.Context, name string) (fs.FileInfo, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "stat", Path: name, Err: fs.ErrInvalid}
	}
	if name == "." {
		return fskit.Entry(".", fs.ModeDir|0555).Info()
	}
	if e, ok := f.byPath[name]; ok {
		return fskit.Entry(path.Base(name), 0555, e.size).Info()
	}
	if ents := f.dirEntries(name); len(ents) > 0 {
		return fskit.Entry(path.Base(name), fs.ModeDir|0555).Info()
	}
	return nil, &fs.PathError{Op: "stat", Path: name, Err: fs.ErrNotExist}
}

func (f *FS) Stat(name string) (fs.FileInfo, error) {
	return f.StatContext(context.Background(), name)
}
