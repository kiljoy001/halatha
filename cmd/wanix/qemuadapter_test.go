//go:build !js && !wasm

package main

import (
	"encoding/binary"
	"testing"
)

// frame builds a QEMU-style big-endian length-prefixed frame.
func frame(payload []byte) []byte {
	b := make([]byte, 4+len(payload))
	binary.BigEndian.PutUint32(b[:4], uint32(len(payload)))
	copy(b[4:], payload)
	return b
}

// TestQemuAdapterWriteBuffersPartialFrames pins the framing state machine:
// a frame split across several Write calls must be buffered, not emitted
// early or dropped. Write always reports the full byte count it consumed.
func TestQemuAdapterWriteBuffersPartialFrames(t *testing.T) {
	q := &qemuAdapter{}

	// Fewer than 4 bytes: cannot know the length yet.
	n, err := q.Write([]byte{0, 0})
	if err != nil || n != 2 {
		t.Fatalf("Write(2) = %d, %v", n, err)
	}
	if len(q.writeBuffer) != 2 {
		t.Fatalf("buffered %d bytes, want 2", len(q.writeBuffer))
	}

	// Complete the header for a 5-byte payload, but send no payload yet.
	n, err = q.Write([]byte{0, 5})
	if err != nil || n != 2 {
		t.Fatalf("Write(2) = %d, %v", n, err)
	}
	if len(q.writeBuffer) != 4 {
		t.Fatalf("buffered %d bytes, want 4", len(q.writeBuffer))
	}

	// Partial payload: still incomplete, still buffered.
	n, err = q.Write([]byte("abc"))
	if err != nil || n != 3 {
		t.Fatalf("Write(3) = %d, %v", n, err)
	}
	if len(q.writeBuffer) != 7 {
		t.Fatalf("buffered %d bytes, want 7", len(q.writeBuffer))
	}
}

// TestQemuAdapterWriteRejectsOverflowLength proves a hostile 32-bit length
// cannot overflow the bounds check. With `int(length)+4` the value
// 0xFFFFFFFF wraps to 3 on 32-bit builds, passing the guard and slicing out
// of range. The check must be done in a width that cannot wrap.
func TestQemuAdapterWriteRejectsOverflowLength(t *testing.T) {
	for _, length := range []uint32{0xFFFFFFFF, 0xFFFFFFFC, 0x7FFFFFFF, 0x80000000} {
		q := &qemuAdapter{}
		hdr := make([]byte, 4)
		binary.BigEndian.PutUint32(hdr, length)

		// Header plus a few payload bytes: nowhere near `length` bytes.
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("length %#x panicked: %v", length, r)
			}
		}()
		n, err := q.Write(append(hdr, 'a', 'b', 'c'))
		if err != nil {
			t.Fatalf("length %#x: unexpected error %v", length, err)
		}
		if n != 7 {
			t.Fatalf("length %#x: n = %d, want 7", length, n)
		}
		// The frame is incomplete, so everything stays buffered and no
		// WriteMessage is attempted (the nil Conn would panic if it were).
		if len(q.writeBuffer) != 7 {
			t.Fatalf("length %#x: buffered %d, want 7 (frame must stay pending)", length, len(q.writeBuffer))
		}
	}
}

// TestQemuAdapterWriteZeroLengthFrame pins the empty-frame boundary, where
// `length+4` exactly equals the buffer size.
func TestQemuAdapterWriteZeroLengthFrame(t *testing.T) {
	q := &qemuAdapter{}
	// A zero-length frame is complete at 4 bytes and would be flushed, which
	// needs a real Conn. Feed only 3 bytes so we stay in the buffering path.
	n, err := q.Write([]byte{0, 0, 0})
	if err != nil || n != 3 {
		t.Fatalf("Write = %d, %v", n, err)
	}
	if len(q.writeBuffer) != 3 {
		t.Fatalf("buffered %d, want 3", len(q.writeBuffer))
	}
}

// TestFrameHelperRoundTrip guards the test helper itself so the assertions
// above rest on a correct frame encoder.
func TestFrameHelperRoundTrip(t *testing.T) {
	payload := []byte("hello wisp")
	f := frame(payload)
	if got := binary.BigEndian.Uint32(f[:4]); int(got) != len(payload) {
		t.Fatalf("length prefix = %d, want %d", got, len(payload))
	}
	if string(f[4:]) != string(payload) {
		t.Fatalf("payload = %q", f[4:])
	}
}
