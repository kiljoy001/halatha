//go:build !js && !wasm

// Package wisp implements the WISP relay protocol: a WebSocket→TCP bridge used
// by v86's "wisp://" network backend (v86/lib.js Vb). The relay accepts one
// WebSocket per VM and multiplexes many TCP streams over it by stream_id.
//
// Wire format (verified against v86/lib.js:3765-3836): every integer is
// LITTLE-ENDIAN. A frame is [type:u8][stream_id:u32 LE][type-specific body…].
//
//	CONNECT  (1)  [proto:u8=1(TCP)][port:u16 LE][hostname UTF-8]   client→server
//	DATA     (2)  [payload…]                                           both dirs
//	CONTINUE (3)  [credit:u32 LE]                                   server→client
//	CLOSE    (4)  [reason:u8]                                          both dirs
//
// The v86 client gates every outgoing frame on a per-stream congestion credit
// (connections[c].congestion, initialized to 0). The server MUST send
// CONTINUE(stream 0, large) on WebSocket open or the client buffers even its
// CONNECT frames and the session stalls silently. See relay.go.
package wisp

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// Frame type bytes (v86/lib.js: switch on a[0]).
const (
	TypeConnect  = 1
	TypeData     = 2
	TypeContinue = 3
	TypeClose    = 4
)

// CONNECT protocol byte at offset 5. v86 only sends 1 (TCP).
const ProtoTCP = 1

// CLOSE reason codes. v86 ignores the value (its close_callback takes no arg),
// so these are informational; 1 is used for any relay-initiated close.
const (
	CloseNormal = 1
	ClosePeer   = 2
)

// MaxFrameSize bounds a single WebSocket message. v86 sends DATA frames sized by
// its TCP write batching; 64 KiB is well above any single send and guards against
// pathological/large frames.
const MaxFrameSize = 64 * 1024

// Frame is a decoded WISP frame. Payload is the type-specific body after the
// 5-byte [type][stream_id] header; the caller interprets it per Type.
type Frame struct {
	Type     uint8
	StreamID uint32
	Payload  []byte
}

// EncodeConnect builds a CONNECT frame: [1][stream_id LE][proto][port LE][host].
func EncodeConnect(streamID uint32, proto uint8, port uint16, host string) []byte {
	hb := []byte(host)
	b := make([]byte, 8+len(hb))
	b[0] = TypeConnect
	binary.LittleEndian.PutUint32(b[1:5], streamID)
	b[5] = proto
	binary.LittleEndian.PutUint16(b[6:8], port)
	copy(b[8:], hb)
	return b
}

// EncodeData builds a DATA frame: [2][stream_id LE][payload].
func EncodeData(streamID uint32, payload []byte) []byte {
	b := make([]byte, 5+len(payload))
	b[0] = TypeData
	binary.LittleEndian.PutUint32(b[1:5], streamID)
	copy(b[5:], payload)
	return b
}

// EncodeContinue builds a CONTINUE frame: [3][stream_id LE][credit LE].
func EncodeContinue(streamID uint32, credit uint32) []byte {
	b := make([]byte, 9)
	b[0] = TypeContinue
	binary.LittleEndian.PutUint32(b[1:5], streamID)
	binary.LittleEndian.PutUint32(b[5:9], credit)
	return b
}

// EncodeClose builds a CLOSE frame: [4][stream_id LE][reason].
func EncodeClose(streamID uint32, reason uint8) []byte {
	b := make([]byte, 6)
	b[0] = TypeClose
	binary.LittleEndian.PutUint32(b[1:5], streamID)
	b[5] = reason
	return b
}

// ReadFrame decodes a raw WebSocket message into a Frame. Payload aliases the
// input slice's tail; do not retain across modifications to msg. The caller
// interprets Payload per Type (see ParseConnect, ParseContinue).
func ReadFrame(msg []byte) (Frame, error) {
	if len(msg) < 5 {
		return Frame{}, fmt.Errorf("wisp: frame too short (%d bytes)", len(msg))
	}
	if len(msg) > MaxFrameSize {
		return Frame{}, fmt.Errorf("wisp: frame too large (%d bytes)", len(msg))
	}
	return Frame{
		Type:     msg[0],
		StreamID: binary.LittleEndian.Uint32(msg[1:5]),
		Payload:  msg[5:],
	}, nil
}

// ParseConnect extracts (proto, port, host) from a CONNECT frame's Payload.
func ParseConnect(p []byte) (proto uint8, port uint16, host string, err error) {
	// Payload layout: [proto:u8][port:u16 LE][hostname…]. Needs ≥3 bytes.
	if len(p) < 3 {
		return 0, 0, "", errors.New("wisp: CONNECT body too short")
	}
	proto = p[0]
	port = binary.LittleEndian.Uint16(p[1:3])
	host = string(p[3:])
	return proto, port, host, nil
}

// ParseContinue extracts the congestion credit from a CONTINUE frame's Payload.
func ParseContinue(p []byte) (uint32, error) {
	if len(p) < 4 {
		return 0, errors.New("wisp: CONTINUE body too short")
	}
	return binary.LittleEndian.Uint32(p[:4]), nil
}

// ParseClose extracts the reason from a CLOSE frame's Payload.
func ParseClose(p []byte) (uint8, error) {
	if len(p) < 1 {
		return 0, errors.New("wisp: CLOSE body too short")
	}
	return p[0], nil
}
