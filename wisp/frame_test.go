//go:build !js && !wasm

package wisp

import (
	"bytes"
	"testing"
)

// Golden bytes hand-derived from v86/lib.js:3786-3805 (all little-endian).
func TestEncodeGolden(t *testing.T) {
	t.Run("CONNECT", func(t *testing.T) {
		got := EncodeConnect(7, ProtoTCP, 9001, "1.2.3.4")
		want := []byte{
			0x01,                   // type
			0x07, 0x00, 0x00, 0x00, // stream_id=7 LE
			0x01,       // proto=TCP
			0x29, 0x23, // port=9001 LE (9001 = 0x2329)
			0x31, 0x2E, 0x32, 0x2E, 0x33, 0x2E, 0x34, // "1.2.3.4"
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("CONNECT = % x, want % x", got, want)
		}
	})

	t.Run("DATA", func(t *testing.T) {
		got := EncodeData(7, []byte("hello"))
		want := []byte{
			0x02,                   // type
			0x07, 0x00, 0x00, 0x00, // stream_id=7 LE
			0x68, 0x65, 0x6C, 0x6C, 0x6F, // "hello"
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("DATA = % x, want % x", got, want)
		}
	})

	t.Run("CONTINUE", func(t *testing.T) {
		got := EncodeContinue(0, 0xFFFFFFFF)
		want := []byte{
			0x03,                   // type
			0x00, 0x00, 0x00, 0x00, // stream_id=0 LE
			0xFF, 0xFF, 0xFF, 0xFF, // credit LE
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("CONTINUE = % x, want % x", got, want)
		}
	})

	t.Run("CLOSE", func(t *testing.T) {
		got := EncodeClose(7, ClosePeer)
		want := []byte{
			0x04,                   // type
			0x07, 0x00, 0x00, 0x00, // stream_id=7 LE
			0x02, // reason=2
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("CLOSE = % x, want % x", got, want)
		}
	})
}

func TestReadFrameRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		buf  []byte
		typ  uint8
		sid  uint32
	}{
		{"CONNECT", EncodeConnect(7, ProtoTCP, 9001, "1.2.3.4"), TypeConnect, 7},
		{"DATA", EncodeData(42, []byte("ping")), TypeData, 42},
		{"CONTINUE", EncodeContinue(0, 123), TypeContinue, 0},
		{"CLOSE", EncodeClose(99, CloseNormal), TypeClose, 99},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f, err := ReadFrame(c.buf)
			if err != nil {
				t.Fatal(err)
			}
			if f.Type != c.typ || f.StreamID != c.sid {
				t.Fatalf("got type=%d sid=%d, want %d/%d", f.Type, f.StreamID, c.typ, c.sid)
			}
		})
	}
}

func TestParseConnect(t *testing.T) {
	f, err := ReadFrame(EncodeConnect(7, ProtoTCP, 9001, "1.2.3.4"))
	if err != nil {
		t.Fatal(err)
	}
	proto, port, host, err := ParseConnect(f.Payload)
	if err != nil {
		t.Fatal(err)
	}
	if proto != ProtoTCP || port != 9001 || host != "1.2.3.4" {
		t.Fatalf("got proto=%d port=%d host=%q", proto, port, host)
	}
}

func TestParseContinue(t *testing.T) {
	f, err := ReadFrame(EncodeContinue(0, 0xFFFFFFFF))
	if err != nil {
		t.Fatal(err)
	}
	credit, err := ParseContinue(f.Payload)
	if err != nil {
		t.Fatal(err)
	}
	if credit != 0xFFFFFFFF {
		t.Fatalf("got credit=%d, want 0xFFFFFFFF", credit)
	}
}

func TestReadFrameShort(t *testing.T) {
	if _, err := ReadFrame([]byte{0x01, 0x02, 0x03}); err == nil {
		t.Fatal("expected error for short frame")
	}
}
