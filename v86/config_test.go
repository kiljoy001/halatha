//go:build js && wasm

package main

import (
	"testing"
)

// TestParseMemorySize pins memory parsing. A wrong multiplier here silently
// gives the guest the wrong RAM, which shows up as an obscure boot failure.
func TestParseMemorySize(t *testing.T) {
	const (
		kb = 1024
		mb = 1024 * 1024
		gb = 1024 * 1024 * 1024
	)
	cases := []struct {
		in      string
		want    int
		wantErr bool
	}{
		{"", 0, false},
		{"512M", 512 * mb, false},
		{"512m", 512 * mb, false}, // case insensitive
		{"1G", gb, false},
		{"1024K", 1024 * kb, false},
		{"128", 128 * mb, false}, // bare number defaults to MB
		{"1.5G", gb + gb/2, false},
		{"0", 0, false},
		// Invalid forms must error, not silently yield a wrong size.
		{"512MB", 0, true}, // the regex allows only a single unit letter
		{"abc", 0, true},
		{"-1M", 0, true},
		{"1.2.3M", 0, true},
		{"M", 0, true},
		{"512 M", 0, true},
	}
	for _, tc := range cases {
		got, err := parseMemorySize(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("parseMemorySize(%q) = %d, want error", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseMemorySize(%q) errored: %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("parseMemorySize(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// TestParseBootOrder pins the v86 boot_order nibble encoding. v86 reads the
// FIRST boot device from the LOW nibble, so these constants are not
// interchangeable: getting them backwards boots the wrong device.
func TestParseBootOrder(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"", 0x123},
		{"a", 0x321},  // floppy first
		{"b", 0x321},  // floppy first
		{"c", 0x312},  // hard disk first (v86's hda default)
		{"d", 0x123},  // cdrom first
		{"n", 0x124},  // network first
		{"z", 0x123},  // unknown falls back
		{"cd", 0x312}, // only the first char is significant
		{"dc", 0x123},
	}
	for _, tc := range cases {
		if got := parseBootOrder(tc.in); got != tc.want {
			t.Errorf("parseBootOrder(%q) = %#x, want %#x", tc.in, got, tc.want)
		}
	}
}

// TestParseBootOrderHardDiskMatchesV86Default guards the specific constant
// that boots the Plan 9 disk images.
func TestParseBootOrderHardDiskMatchesV86Default(t *testing.T) {
	if got := parseBootOrder("c"); got != 0x312 {
		t.Fatalf("hard-disk boot order = %#x, want 0x312 (v86 hda default)", got)
	}
}

// TestParseVirtfs pins VirtFS config parsing, including the modes that must
// be rejected rather than half-configured.
func TestParseVirtfs(t *testing.T) {
	if got := parseVirtfs(""); got != nil {
		t.Errorf("empty = %v, want nil", got)
	}
	if got := parseVirtfs("proxy"); got != nil {
		t.Errorf("missing url = %v, want nil", got)
	}
	if got := parseVirtfs("local,/tmp"); got != nil {
		t.Errorf("non-proxy mode = %v, want nil", got)
	}

	got := parseVirtfs("proxy,http://localhost:8080")
	if got == nil {
		t.Fatal("valid proxy config returned nil")
	}
	if got["proxy_url"] != "http://localhost:8080" {
		t.Errorf("proxy_url = %v", got["proxy_url"])
	}

	// Extra fields are ignored, not mis-assigned.
	got = parseVirtfs("proxy,http://x,extra,more")
	if got["proxy_url"] != "http://x" {
		t.Errorf("proxy_url = %v, want http://x", got["proxy_url"])
	}
}
