//go:build ignore_test

// Tests for mk9legacyimg.go.
//
// HOW TO RUN: this directory cannot be built by `go test` directly, because
// it contains serial9p.c (a Plan 9 kernel driver) and the toolchain refuses
// C files without cgo. mk9legacyimg.go is also `//go:build ignore` since it
// runs via `go run`. Copy both files to a scratch module, strip the build
// tags, and test there:
//
//	d=$(mktemp -d)
//	sed 's|^//go:build ignore$||' extras/9front/mk9legacyimg.go > $d/m.go
//	sed 's|^//go:build ignore_test$||' extras/9front/mk9legacyimg_test.go > $d/m_test.go
//	printf 'module mkimgtest\n\ngo 1.26\n' > $d/go.mod
//	(cd $d && go test ./... && go test -fuzz FuzzBLZRoundTrip -fuzztime 90s .)
//
// Verified 2026-09-21: 21.8M round-trip execs and 3.9M decoder execs, clean.
//
// The BLZ codec rewrites the gzroot filesystem inside the boot image, so a
// pack/unpack asymmetry corrupts the disk silently. Round-tripping is the
// property that actually matters.
package main

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"
)

func TestBLZRoundTripKnownInputs(t *testing.T) {
	cases := map[string][]byte{
		"empty":            {},
		"single byte":      {0x41},
		"no repetition":    []byte("abcdefghijklmnopqrstuvwxyz"),
		"all zeros":        make([]byte, 4096),
		"long run":         bytes.Repeat([]byte{0xAA}, 70000),
		"repeating phrase": bytes.Repeat([]byte("halatha/plan9 "), 500),
		"binary-ish": func() []byte {
			b := make([]byte, 8192)
			for i := range b {
				b[i] = byte(i * 7)
			}
			return b
		}(),
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			packed, err := packBLZ(in)
			if err != nil {
				t.Fatalf("packBLZ: %v", err)
			}
			if !bytes.HasPrefix(packed, []byte("BLZ\n")) {
				t.Fatal("packed output lacks the BLZ magic")
			}
			if got := binary.BigEndian.Uint32(packed[4:8]); int(got) != len(in) {
				t.Fatalf("header length = %d, want %d", got, len(in))
			}
			out, err := unpackBLZ(packed)
			if err != nil {
				t.Fatalf("unpackBLZ: %v", err)
			}
			if !bytes.Equal(out, in) {
				t.Fatalf("round trip corrupted data: got %d bytes, want %d", len(out), len(in))
			}
		})
	}
}

func TestBLZRawRoundTrip(t *testing.T) {
	for _, in := range [][]byte{{}, []byte("x"), bytes.Repeat([]byte("q"), 1000)} {
		packed, err := packBLZRaw(in)
		if err != nil {
			t.Fatalf("packBLZRaw: %v", err)
		}
		out, err := unpackBLZ(packed)
		if err != nil {
			t.Fatalf("unpackBLZ: %v", err)
		}
		if !bytes.Equal(out, in) {
			t.Fatalf("raw round trip corrupted %d bytes", len(in))
		}
	}
}

// TestUnpackBLZRejectsMalformed pins the decoder's bounds checks. It parses
// data from an existing disk image, so it must fail cleanly rather than
// panic or over-read on a truncated or hostile stream.
func TestUnpackBLZRejectsMalformed(t *testing.T) {
	good, err := packBLZ([]byte("some representative payload data"))
	if err != nil {
		t.Fatal(err)
	}

	cases := map[string][]byte{
		"empty":           {},
		"too short":       []byte("BLZ"),
		"bad magic":       append([]byte("XXX\n"), good[4:]...),
		"header only":     good[:8],
		"truncated table": good[:10],
		"truncated body":  good[:len(good)-3],
		"huge declared": func() []byte {
			b := append([]byte{}, good...)
			binary.BigEndian.PutUint32(b[4:8], 0xFFFFFFF0)
			return b
		}(),
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("unpackBLZ panicked on %s: %v", name, r)
				}
			}()
			if _, err := unpackBLZ(in); err == nil {
				t.Errorf("malformed input %q was accepted", name)
			}
		})
	}
}

// FuzzBLZRoundTrip is the real property: for ANY input, unpack(pack(x)) == x.
func FuzzBLZRoundTrip(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte("abc"))
	f.Add(bytes.Repeat([]byte{0}, 300))
	f.Add(bytes.Repeat([]byte("ab"), 200))
	f.Add([]byte("aaaaaaaaaabbbbbbbbbbaaaaaaaaaa"))

	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<20 {
			return
		}
		packed, err := packBLZ(data)
		if err != nil {
			t.Fatalf("packBLZ failed on %d bytes: %v", len(data), err)
		}
		out, err := unpackBLZ(packed)
		if err != nil {
			t.Fatalf("unpackBLZ rejected our own output (%d bytes): %v", len(data), err)
		}
		if !bytes.Equal(out, data) {
			t.Fatalf("round trip mismatch: in %d bytes, out %d bytes", len(data), len(out))
		}
	})
}

// FuzzUnpackBLZNoPanic feeds arbitrary bytes to the decoder. It reads an
// on-disk image, so it must never panic however corrupt that image is.
func FuzzUnpackBLZNoPanic(f *testing.F) {
	f.Add([]byte("BLZ\n"))
	f.Add([]byte("BLZ\n\x00\x00\x00\x04"))
	f.Add([]byte("BLZ\n\x00\x00\x00\x04\x80\x00\x00\x04abcd"))
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = unpackBLZ(data) // must not panic
	})
}

// TestPlan9INIPatching pins the boot config rewrites. A wrong menudefault or
// a duplicated key breaks the boot menu, which is hard to debug in v86.
func TestPlan9INIPatching(t *testing.T) {
	const base = "[common]\nbootfile=9pcflop\n\n[menu]\nmenuitem=1\n"

	got := string(patchPlan9INIContents([]byte(base), "3", false, false))
	if !strings.Contains(got, "menudefault=3") {
		t.Errorf("menudefault not set:\n%s", got)
	}
	// Duplicate keys across [common] and a section hang getconf, so the
	// key must appear exactly once.
	if n := strings.Count(got, "menudefault="); n != 1 {
		t.Errorf("menudefault appears %d times, want 1:\n%s", n, got)
	}

	// usbBoot must set the bare name: a `*`-prefixed var is invisible to
	// boot's getenv.
	usb := string(patchPlan9INIContents([]byte(base), "1", false, true))
	if strings.Contains(usb, "*wanixusb") {
		t.Errorf("usb flag written with a `*` prefix, which getenv cannot see:\n%s", usb)
	}
}

// TestWithoutSerialConsole pins removal of the serial console directive.
// PLAN9.INI carries it as the single line "console=0 b115200"; leaving it in
// sends the console to the serial port and the VESA display stays blank.
func TestWithoutSerialConsole(t *testing.T) {
	in := []byte("[common]\nconsole=0 b115200\nbootfile=9pcflop\n")
	out := string(withoutSerialConsole(in))
	if strings.Contains(out, "console=0") {
		t.Errorf("serial console line not removed:\n%s", out)
	}
	if !strings.Contains(out, "bootfile=9pcflop") {
		t.Errorf("unrelated line was dropped:\n%s", out)
	}

	// A line that merely resembles it must survive untouched.
	keep := []byte("[common]\nconsole=0 b9600\n")
	if !strings.Contains(string(withoutSerialConsole(keep)), "b9600") {
		t.Error("a different console line was incorrectly dropped")
	}
}

// TestCleanArchivePath pins archive path normalisation. Paths in the 9legacy
// archive are absolute by format ("/rc/bin/..."), so this rooted-Clean is
// the containment mechanism: traversal is collapsed against "/" and can
// never reach outside the archive.
func TestCleanArchivePath(t *testing.T) {
	cases := map[string]string{
		"rc/bin/halatha.local": "/rc/bin/halatha.local",
		"386/bin/o9c":          "/386/bin/o9c",
		"/already/abs":         "/already/abs",
		`windows\style\path`: "/windows/style/path",
		// Traversal is collapsed at the root, not allowed to escape it.
		"../etc/passwd": "/etc/passwd",
		"a/../../b":     "/b",
		"./x":           "/x",
	}
	for in, want := range cases {
		got, err := cleanArchivePath(in)
		if err != nil {
			t.Errorf("cleanArchivePath(%q) errored: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("cleanArchivePath(%q) = %q, want %q", in, got, want)
		}
		if !strings.HasPrefix(got, "/") {
			t.Errorf("cleanArchivePath(%q) = %q is not rooted", in, got)
		}
	}

	// Degenerate paths must be refused outright.
	for _, bad := range []string{"", "   ", "/", "..", "/.."} {
		if got, err := cleanArchivePath(bad); err == nil {
			t.Errorf("cleanArchivePath(%q) = %q, want error", bad, got)
		}
	}
}
