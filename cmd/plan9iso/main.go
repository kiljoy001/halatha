// Command plan9iso extracts a Plan 9 ISO 9660 image into a directory, keeping
// the Plan 9 names and permission bits that mk9660 stores in each directory
// record's system use area. Joliet and Rock Ridge readers do not see that
// extension, so their trees lose the exec bits and nothing in /386/bin can be
// run when the tree is served to a guest over 9P.
//
//	plan9iso local/9front-11957.386.iso local/9front-root
//
// The record layout follows 9660srv's rzdir: after the ISO name, padded to an
// even offset, come the name, uid and gid as length-prefixed strings, padding
// to an even offset, then the mode as a little-endian uint32.
package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	sector = 2048
	dmDir  = 0x80000000
)

type record struct {
	name, uid, gid string
	mode           uint32
	extent, size   uint32
	dir            bool
}

type stats struct {
	files, dirs int
	bytes       int64
}

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: plan9iso image.iso outdir")
		os.Exit(2)
	}
	st, err := extract(os.Args[1], os.Args[2])
	if err != nil {
		fmt.Fprintln(os.Stderr, "plan9iso:", err)
		os.Exit(1)
	}
	fmt.Printf("plan9iso: %d files, %d directories, %d bytes -> %s\n", st.files, st.dirs, st.bytes, os.Args[2])
}

// parseRecord decodes one directory record. Every length is checked against
// the record, because the bytes come from the image.
func parseRecord(b []byte) (record, error) {
	var r record
	if len(b) < 34 || int(b[0]) < 34 || int(b[0]) > len(b) {
		return r, errors.New("short directory record")
	}
	b = b[:b[0]]
	r.extent = binary.LittleEndian.Uint32(b[2:])
	r.size = binary.LittleEndian.Uint32(b[10:])
	r.dir = b[25]&2 != 0
	if b[25]&0x80 != 0 {
		return r, errors.New("multi-extent files are not supported")
	}
	nl := int(b[32])
	if 33+nl > len(b) {
		return r, errors.New("name overruns record")
	}
	isoName := string(b[33 : 33+nl])

	s := 33 + nl
	if s%2 == 1 {
		s++
	}
	var fields [3]string
	for i := range fields {
		if s >= len(b) || s+1+int(b[s]) > len(b) {
			return r, errors.New("record has no Plan 9 extension")
		}
		fields[i] = string(b[s+1 : s+1+int(b[s])])
		s += 1 + int(b[s])
	}
	if s%2 == 1 {
		s++
	}
	if s+4 > len(b) {
		return r, errors.New("record has no Plan 9 mode")
	}
	r.mode = binary.LittleEndian.Uint32(b[s:])
	r.uid, r.gid = fields[1], fields[2]
	if r.mode&dmDir != 0 {
		r.dir = true
	}

	r.name = fields[0]
	if r.name == "" {
		r.name = strings.ToLower(strings.TrimSuffix(strings.SplitN(isoName, ";", 2)[0], "."))
	}
	if r.name == "" || r.name == "." || r.name == ".." || strings.ContainsAny(r.name, "/\x00") {
		return r, fmt.Errorf("unsafe name %q", r.name)
	}
	return r, nil
}

// extract writes the image's tree to out, which must not exist yet.
func extract(image, out string) (stats, error) {
	var st stats
	f, err := os.Open(image)
	if err != nil {
		return st, err
	}
	defer f.Close()
	if _, err := os.Stat(out); err == nil {
		return st, fmt.Errorf("%s already exists; refusing to overwrite", out)
	}

	root, err := findRoot(f)
	if err != nil {
		return st, err
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		return st, err
	}
	if err := walk(f, root, out, &st, 0); err != nil {
		return st, err
	}
	return st, nil
}

// findRoot reads the primary volume descriptor, which holds the root
// directory record. Its system identifier (offset 8) must start with
// "PLAN 9", the test 9660srv makes before it trusts the extension.
func findRoot(f io.ReaderAt) (record, error) {
	var root record
	plan9, havePrimary := false, false
	buf := make([]byte, sector)
	for i := int64(16); i < 16+64; i++ {
		if _, err := f.ReadAt(buf, i*sector); err != nil {
			return root, err
		}
		if string(buf[1:6]) != "CD001" {
			return root, errors.New("not an ISO 9660 image")
		}
		switch buf[0] {
		case 1: // primary volume descriptor
			plan9 = bytes.HasPrefix(buf[8:40], []byte("PLAN 9"))
			rec := buf[156 : 156+34]
			root.extent = binary.LittleEndian.Uint32(rec[2:])
			root.size = binary.LittleEndian.Uint32(rec[10:])
			root.dir = true
			havePrimary = true
		case 255: // terminator
			i = 16 + 64
		}
	}
	if !havePrimary {
		return root, errors.New("no primary volume descriptor")
	}
	if !plan9 {
		return root, errors.New("not a Plan 9 image: system identifier is not PLAN 9, so there are no modes to keep")
	}
	return root, nil
}

func walk(f io.ReaderAt, dir record, out string, st *stats, depth int) error {
	if depth > 64 {
		return errors.New("directory tree too deep")
	}
	data := make([]byte, dir.size)
	if _, err := f.ReadAt(data, int64(dir.extent)*sector); err != nil {
		return err
	}
	for off := 0; off < len(data); {
		n := int(data[off])
		if n == 0 { // records never span sectors; skip the padding
			off = (off/sector + 1) * sector
			continue
		}
		if off+n > len(data) {
			return fmt.Errorf("%s: record overruns directory", out)
		}
		raw := data[off : off+n]
		off += n
		if raw[32] == 1 && (raw[33] == 0 || raw[33] == 1) {
			continue // "." and ".."
		}
		r, err := parseRecord(raw)
		if err != nil {
			return fmt.Errorf("%s: %w", out, err)
		}
		p := filepath.Join(out, r.name)
		if r.dir {
			st.dirs++
			if err := os.Mkdir(p, 0o755); err != nil {
				return err
			}
			if err := walk(f, r, p, st, depth+1); err != nil {
				return err
			}
			// Applied after the contents are written, so a read-only
			// directory does not block its own extraction.
			if err := os.Chmod(p, os.FileMode(r.mode&0o777)); err != nil {
				return err
			}
			continue
		}
		st.files++
		st.bytes += int64(r.size)
		if err := writeFile(f, r, p); err != nil {
			return err
		}
	}
	return nil
}

func writeFile(f io.ReaderAt, r record, p string) error {
	w, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, os.FileMode(r.mode&0o777)|0o200)
	if err != nil {
		return err
	}
	if _, err := io.Copy(w, io.NewSectionReader(f, int64(r.extent)*sector, int64(r.size))); err != nil {
		w.Close()
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return os.Chmod(p, os.FileMode(r.mode&0o777))
}
