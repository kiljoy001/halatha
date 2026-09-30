//go:build !js && !wasm

package main

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// drec builds an ISO 9660 directory record the way mk9660 writes one for a
// Plan 9 image: the ISO name, then in the system use area the real name,
// uid and gid as length-prefixed strings, padded to an even offset, then the
// Plan 9 mode as a little-endian uint32.
func drec(isoName, name, uid, gid string, mode uint32, extent, size uint32, dir bool) []byte {
	r := make([]byte, 33)
	binary.LittleEndian.PutUint32(r[2:], extent)
	binary.BigEndian.PutUint32(r[6:], extent)
	binary.LittleEndian.PutUint32(r[10:], size)
	binary.BigEndian.PutUint32(r[14:], size)
	if dir {
		r[25] = 2
	}
	r[32] = byte(len(isoName))
	r = append(r, isoName...)
	if len(r)%2 == 1 {
		r = append(r, 0)
	}
	for _, s := range []string{name, uid, gid} {
		r = append(r, byte(len(s)))
		r = append(r, s...)
	}
	if len(r)%2 == 1 {
		r = append(r, 0)
	}
	r = binary.LittleEndian.AppendUint32(r, mode)
	r[0] = byte(len(r))
	return r
}

// The Plan 9 extension carries what Joliet cannot: the real name and the
// mode, whose exec bits decide whether the guest can run the file at all.
func TestParseRecordPlan9Extension(t *testing.T) {
	for _, isoName := range []string{"RC.;1", "BOOTRC"} { // odd and even name lengths
		rec := drec(isoName, "rc", "sys", "sys", 0775, 100, 4096, false)
		got, err := parseRecord(rec)
		if err != nil {
			t.Fatalf("%s: %v", isoName, err)
		}
		if got.name != "rc" || got.uid != "sys" || got.gid != "sys" || got.mode != 0775 ||
			got.extent != 100 || got.size != 4096 || got.dir {
			t.Errorf("%s: parsed %+v", isoName, got)
		}
	}
}

func TestParseRecordDirectory(t *testing.T) {
	rec := drec("386", "386", "sys", "sys", dmDir|0775, 20, 2048, true)
	got, err := parseRecord(rec)
	if err != nil || !got.dir || got.mode&0777 != 0775 {
		t.Errorf("parsed %+v, err %v", got, err)
	}
}

// Records are read from the image, so a damaged or hostile image must yield
// an error, never a panic or an out-of-range read.
func FuzzParseRecord(f *testing.F) {
	f.Add(drec("RC.;1", "rc", "sys", "sys", 0775, 1, 2, false))
	f.Add([]byte{0})
	f.Add([]byte{34, 0, 0, 0})
	f.Fuzz(func(t *testing.T, b []byte) {
		r, err := parseRecord(b)
		if err == nil && (r.name == "" || r.name == "." || r.name == ".." || filepath.Base(r.name) != r.name) {
			t.Fatalf("accepted unsafe name %q", r.name)
		}
	})
}

// A name from the image becomes a path on the host, so it must never be able
// to climb out of the output directory.
func TestParseRecordRejectsUnsafeNames(t *testing.T) {
	for _, name := range []string{"..", ".", "a/b", "../x", ""} {
		rec := drec("X", name, "sys", "sys", 0664, 1, 1, false)
		if r, err := parseRecord(rec); err == nil && name != "" {
			t.Errorf("accepted name %q as %+v", name, r)
		}
	}
}

// An image without the Plan 9 system identifier has no modes to keep. It
// must be refused, not extracted into a tree whose programs cannot run.
func TestExtractRefusesNonPlan9Image(t *testing.T) {
	img := make([]byte, 19*sector)
	pvd := img[16*sector:]
	pvd[0] = 1
	copy(pvd[1:], "CD001")
	pvd[6] = 1
	copy(pvd[8:], "LINUX")
	copy(pvd[156:], drec("\x00", "", "", "", dmDir|0o755, 18, sector, true))
	term := img[17*sector:]
	term[0] = 255
	copy(term[1:], "CD001")
	iso := filepath.Join(t.TempDir(), "linux.iso")
	if err := os.WriteFile(iso, img, 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "root")
	if _, err := extract(iso, out); err == nil {
		t.Fatal("non-Plan 9 image was extracted")
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Errorf("refused image still created %s", out)
	}
}

// The real 9front ISO, when present: the tree must keep the modes that make
// the system bootable and runnable over 9P.
func TestExtractRealISO(t *testing.T) {
	iso := filepath.Join("..", "..", "local", "9front-11957.386.iso")
	if _, err := os.Stat(iso); err != nil {
		t.Skip("9front ISO not present")
	}
	out := filepath.Join(t.TempDir(), "root")
	st, err := extract(iso, out)
	if err != nil {
		t.Fatal(err)
	}
	if st.files < 1000 || st.dirs < 100 {
		t.Errorf("suspiciously small tree: %+v", st)
	}
	for path, want := range map[string]os.FileMode{
		"386/bin/rc":            0o111,
		"rc/bin/termrc":         0o111,
		"386/bin/ls":            0o111,
		"sys/src/9/boot/bootrc": 0,
		"lib/namespace":         0,
	} {
		fi, err := os.Stat(filepath.Join(out, path))
		if err != nil {
			t.Errorf("%s: %v", path, err)
			continue
		}
		if fi.Mode().Perm()&want != want {
			t.Errorf("%s: mode %v, want exec bits %v", path, fi.Mode().Perm(), want)
		}
	}
}
