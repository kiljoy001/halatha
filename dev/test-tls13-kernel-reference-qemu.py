#!/usr/bin/env python3
"""Check 9front's TLS 1.3 kernel records against an independent AES-GCM implementation.

Usage: test-tls13-kernel-reference-qemu.py base.img kernel outdir
"""

import importlib.util
import os
import socket
import subprocess
import sys
import threading
import time
from pathlib import Path

from cryptography.hazmat.primitives.ciphers.aead import AESGCM

REPO = Path(__file__).resolve().parent.parent
spec = importlib.util.spec_from_file_location(
    "kernel_builder", REPO / "extras/9front/kernel/build-kernel.py"
)
builder = importlib.util.module_from_spec(spec)
spec.loader.exec_module(builder)

PLAN9_INI = """bootfile=9pchal
bootargs=local!/dev/sdC0/fs -A
nobootprompt=local!/dev/sdC0/fs -A
user=glenda
console=0 b115200
mouseport=ps2
"""


def stage(base, kernel, outdir):
    if outdir.exists():
        raise RuntimeError(f"{outdir} exists; refusing to overwrite")
    outdir.mkdir(parents=True)
    disk = outdir / "reference.img"
    subprocess.run(["cp", "--sparse=always", str(base), str(disk)], check=True)
    ini = outdir / "plan9.ini"
    ini.write_text(PLAN9_INI)
    fat = f"{disk}@@{builder.ninefat_offset(disk)}"
    subprocess.run(["mmd", "-i", fat, "::halatha"], check=True)
    for src, dest in (
        (kernel, "::9pchal"),
        (ini, "::plan9.ini"),
        (REPO / "dev/test-tls13-kernel.c", "::halatha/tls13test.c"),
    ):
        subprocess.run(["mcopy", "-o", "-i", fat, str(src), dest], check=True)
    return disk


def read_exact(conn, length):
    result = bytearray()
    while len(result) < length:
        part = conn.recv(length - len(result))
        if not part:
            raise RuntimeError(f"EOF after {len(result)} of {length} bytes")
        result.extend(part)
    return bytes(result)


def nonce(iv, seq):
    return bytes(a ^ b for a, b in zip(iv, seq.to_bytes(12, "big")))


def read_record(conn, key, iv, seq):
    header = read_exact(conn, 5)
    if header[:3] != b"\x17\x03\x03":
        raise RuntimeError(f"wrong TLS 1.3 outer header: {header.hex()}")
    length = int.from_bytes(header[3:], "big")
    if not 17 <= length <= 16401:
        raise RuntimeError(f"wrong TLS 1.3 record length: {length}")
    plaintext = AESGCM(key).decrypt(
        nonce(iv, seq), read_exact(conn, length), header
    ).rstrip(b"\x00")
    if not plaintext or plaintext[-1] != 23:
        raise RuntimeError("wrong TLS 1.3 inner content type")
    return plaintext[:-1]


def write_record(conn, key, iv, seq, data, content_type=23, padding=0):
    header = b"\x17\x03\x03" + (len(data) + 1 + padding + 16).to_bytes(2, "big")
    ciphertext = AESGCM(key).encrypt(
        nonce(iv, seq), data + bytes([content_type]) + bytes(padding), header
    )
    conn.sendall(header + ciphertext)


def reference_server(ready, problems):
    material = bytes(range(56))
    client_key, server_key = material[:16], material[16:32]
    client_iv, server_iv = material[32:44], material[44:56]

    def exchange(conn, client_key, server_key, client_iv, server_iv):
        conn.settimeout(30)
        if read_record(conn, client_key, client_iv, 0) != b"hello":
            raise RuntimeError("wrong first client record")
        write_record(conn, server_key, server_iv, 0, b"world")
        bulk = bytearray()
        seq = 1
        while len(bulk) < 32768:
            bulk.extend(read_record(conn, client_key, client_iv, seq))
            seq += 1
        if bulk != bytes(i & 255 for i in range(32768)):
            raise RuntimeError("wrong client bulk content")
        write_record(conn, server_key, server_iv, 1, bulk[:16384])
        write_record(conn, server_key, server_iv, 2, bulk[16384:])

    try:
        with socket.socket() as listener:
            listener.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
            listener.bind(("127.0.0.1", 16443))
            listener.listen(4)
            listener.settimeout(90)
            ready.set()
            conn, _ = listener.accept()
            with conn:
                exchange(conn, client_key, server_key, client_iv, server_iv)
            conn, _ = listener.accept()
            with conn:
                conn.settimeout(30)
                conn.sendall(b"\x14\x03\x03\x00\x01\x01")
                write_record(conn, server_key, server_iv, 0, b"ready", 22, 8)
                write_record(conn, server_key, server_iv, 1, b"world", padding=5)
            material256 = bytes(range(88))
            conn, _ = listener.accept()
            with conn:
                exchange(conn, material256[:32], material256[32:64],
                         material256[64:76], material256[76:88])
            conn, _ = listener.accept()
            with conn:
                conn.sendall(b"\x16\x03\x01\x00\x05hello")
    except Exception as error:
        problems.append(error)
        ready.set()


def main():
    if len(sys.argv) != 4:
        sys.exit("usage: test-tls13-kernel-reference-qemu.py base.img kernel outdir")
    disk = stage(Path(sys.argv[1]).resolve(), Path(sys.argv[2]).resolve(),
                 Path(sys.argv[3]).resolve())
    logpath = disk.with_name("reference-serial.log")
    ready = threading.Event()
    problems = []
    thread = threading.Thread(target=reference_server, args=(ready, problems), daemon=True)
    thread.start()
    if not ready.wait(5) or problems:
        raise RuntimeError(f"reference server failed: {problems}")
    qemu = [
        "qemu-system-i386", "-m", "1024", "-display", "none",
        "-monitor", "none", "-serial", "stdio", "-no-reboot",
        "-netdev", "user,id=n0",
        "-device", "ne2k_pci,netdev=n0,mac=52:54:00:44:13:03",
        "-drive", f"file={disk},format=raw,if=ide,index=0",
    ]
    if os.access("/dev/kvm", os.R_OK | os.W_OK):
        qemu.insert(1, "-enable-kvm")
    with logpath.open("w") as log:
        proc = subprocess.Popen(qemu, stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                                stderr=subprocess.STDOUT)
        con = builder.Console(proc, log)
        try:
            for _ in range(2):
                if con.expect(["init: starting /bin/rc"], 180) is None:
                    raise RuntimeError("VM did not reach rc")
            if con.expect(["term% "], 60) is None:
                raise RuntimeError("VM did not reach console prompt")

            def run(command, timeout=60, want=None):
                time.sleep(0.5)
                con.buf = ""
                log.flush()
                start = logpath.stat().st_size
                con.send(command)
                if con.expect(["term% "], timeout) is None:
                    raise RuntimeError(f"timed out running {command}")
                log.flush()
                output = logpath.read_bytes()[start:].decode(errors="replace")
                if want is not None and want not in output:
                    raise RuntimeError(f"{command}: {want!r} absent: {output[-1200:]}")
                return output

            run("9fs 9fat", 30)
            run("ip/ipconfig ether /net/ether0 10.0.2.15 255.255.255.0 10.0.2.2", 30)
            run("cp /n/9fat/halatha/tls13test.c /tmp/tls13test.c", 30)
            run("8c -o /tmp/tls13test.8 /tmp/tls13test.c", 60)
            run("8l -o /tmp/tls13test /tmp/tls13test.8", 60)
            run("/tmp/tls13test clienthost", 90, "TLS13 KERNEL CLIENT PASS")
            run("/tmp/tls13test handhost", 30, "TLS13 KERNEL CCS PASS")
            run("/tmp/tls13test clienthost256", 90, "TLS13 KERNEL CLIENT PASS")
            run("/tmp/tls13test plainhost", 30, "TLS13 INITIAL HELLO PASS")
            thread.join(5)
            if thread.is_alive() or problems:
                raise RuntimeError(f"reference server failed: {problems}")
            print("TLS13 KERNEL REFERENCE PASS", flush=True)
        finally:
            proc.kill()
            proc.wait()


if __name__ == "__main__":
    main()
