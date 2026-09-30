package wisp

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// FuzzParseFrames drives every WISP body parser with arbitrary attacker
// controlled bytes. None of them may panic; they must return an error or a
// value. These parse data read straight off the WebSocket, so a panic here
// is remotely triggerable.
func FuzzParseFrames(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{0})
	f.Add([]byte{1, 2, 3})
	f.Add([]byte{1, 2, 3, 4})
	f.Add([]byte{1, 0x50, 0x00, 'e', 'x', 'a', 'm', 'p', 'l', 'e'})
	f.Add(bytes.Repeat([]byte{0xff}, 64))

	f.Fuzz(func(t *testing.T, p []byte) {
		if proto, port, host, err := ParseConnect(p); err == nil {
			// A successful parse must be self-consistent with the input.
			if len(p) < 3 {
				t.Fatalf("ParseConnect accepted %d-byte body", len(p))
			}
			if proto != p[0] {
				t.Fatalf("proto = %d, want %d", proto, p[0])
			}
			if want := binary.LittleEndian.Uint16(p[1:3]); port != want {
				t.Fatalf("port = %d, want %d", port, want)
			}
			if host != string(p[3:]) {
				t.Fatalf("host = %q, want %q", host, string(p[3:]))
			}
		}
		if credit, err := ParseContinue(p); err == nil {
			if len(p) < 4 {
				t.Fatalf("ParseContinue accepted %d-byte body", len(p))
			}
			if want := binary.LittleEndian.Uint32(p[:4]); credit != want {
				t.Fatalf("credit = %d, want %d", credit, want)
			}
		}
		if reason, err := ParseClose(p); err == nil {
			if len(p) < 1 {
				t.Fatalf("ParseClose accepted empty body")
			}
			if reason != p[0] {
				t.Fatalf("reason = %d, want %d", reason, p[0])
			}
		}
	})
}

// FuzzReadFrame feeds arbitrary bytes to the frame reader. It must never
// panic regardless of how malformed the stream is.
func FuzzReadFrame(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{2, 1, 0, 0, 0})
	f.Add([]byte{1, 0, 0, 0, 0, 1, 0x50, 0, 'h'})
	f.Add(bytes.Repeat([]byte{0x02}, 128))

	f.Fuzz(func(t *testing.T, b []byte) {
		fr, err := ReadFrame(b)
		if err != nil {
			return
		}
		// A parsed frame must round-trip its header fields.
		if len(b) < 5 {
			t.Fatalf("ReadFrame accepted %d-byte frame", len(b))
		}
		if fr.Type != b[0] {
			t.Fatalf("type = %d, want %d", fr.Type, b[0])
		}
		if want := binary.LittleEndian.Uint32(b[1:5]); fr.StreamID != want {
			t.Fatalf("streamID = %d, want %d", fr.StreamID, want)
		}
		if !bytes.Equal(fr.Payload, b[5:]) {
			t.Fatalf("payload = %v, want %v", fr.Payload, b[5:])
		}
	})
}
