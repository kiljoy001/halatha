#!/usr/bin/env python3
"""Probe Wanix's local WebSocket exporter with a plain 9P2000 Tversion.

Run on the VPS: python3 dev/probe-native-9p.py [host] [port]
"""
import base64
import os
import socket
import struct
import sys

host = sys.argv[1] if len(sys.argv) > 1 else "127.0.0.1"
port = int(sys.argv[2]) if len(sys.argv) > 2 else 7654


def read_exact(sock, count):
    data = bytearray()
    while len(data) < count:
        chunk = sock.recv(count - len(data))
        if not chunk:
            raise EOFError("WebSocket closed")
        data.extend(chunk)
    return bytes(data)


with socket.create_connection((host, port), timeout=10) as sock:
    key = base64.b64encode(os.urandom(16)).decode()
    request = (
        "GET / HTTP/1.1\r\n"
        "Host: halatha.rentonsoftworks.com\r\n"
        "Origin: https://halatha.rentonsoftworks.com\r\n"
        "Upgrade: websocket\r\n"
        "Connection: Upgrade\r\n"
        f"Sec-WebSocket-Key: {key}\r\n"
        "Sec-WebSocket-Version: 13\r\n\r\n"
    )
    sock.sendall(request.encode())
    response = bytearray()
    while b"\r\n\r\n" not in response and len(response) < 8192:
        response.extend(read_exact(sock, 1))
    status = response.split(b"\r\n", 1)[0]
    if b" 101 " not in status:
        raise RuntimeError(f"WebSocket handshake failed: {status.decode(errors='replace')}")

    version = b"9P2000"
    p9 = struct.pack("<IBHIH", 13 + len(version), 100, 0xFFFF, 8192, len(version)) + version
    mask = os.urandom(4)
    sock.sendall(bytes((0x82, 0x80 | len(p9))) + mask +
                 bytes(byte ^ mask[i % 4] for i, byte in enumerate(p9)))

    first, second = read_exact(sock, 2)
    length = second & 0x7F
    if length == 126:
        length = struct.unpack("!H", read_exact(sock, 2))[0]
    elif length == 127:
        length = struct.unpack("!Q", read_exact(sock, 8))[0]
    response_mask = read_exact(sock, 4) if second & 0x80 else None
    payload = read_exact(sock, length)
    if response_mask:
        payload = bytes(byte ^ response_mask[i % 4] for i, byte in enumerate(payload))
    if first & 0x0F != 2 or len(payload) < 13 or payload[4] != 101:
        raise RuntimeError("Unexpected WebSocket/9P response")
    msize = struct.unpack_from("<I", payload, 7)[0]
    name_len = struct.unpack_from("<H", payload, 11)[0]
    name = payload[13:13 + name_len].decode()
    print(f"native Wanix 9P version: {name}/{msize}")
    if name != "9P2000" or msize < 256:
        sys.exit(1)
