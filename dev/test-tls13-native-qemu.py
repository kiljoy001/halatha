#!/usr/bin/env python3
"""Test 9front's native TLS 1.3 client and kernel against a Python TLS server."""

import hashlib
import importlib.util
import os
import socket
import ssl
import subprocess
import sys
import threading
import time
from pathlib import Path

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


def stage(base, release, outdir):
    if outdir.exists():
        raise RuntimeError(f"{outdir} exists; refusing to overwrite")
    outdir.mkdir(parents=True)
    subprocess.run([
        "openssl", "ecparam", "-name", "prime256v1", "-genkey",
        "-noout", "-out", str(outdir / "server.key"),
    ], check=True)
    subprocess.run([
        "openssl", "req", "-new", "-x509", "-sha256",
        "-key", str(outdir / "server.key"),
        "-out", str(outdir / "server.crt"), "-days", "1",
        "-subj", "/CN=tls13-vm.local",
        "-addext", "subjectAltName=DNS:tls13-vm.local",
    ], check=True)
    der = ssl.PEM_cert_to_DER_cert((outdir / "server.crt").read_text())
    (outdir / "trust").write_text(
        "x509 sha256=" + hashlib.sha256(der).hexdigest() + "\n"
    )
    (outdir / "wrong-trust").write_text("x509 sha256=" + "00" * 32 + "\n")
    disk = outdir / "native.img"
    subprocess.run(["cp", "--sparse=always", str(base), str(disk)], check=True)
    ini = outdir / "plan9.ini"
    ini.write_text(PLAN9_INI)
    fat = f"{disk}@@{builder.ninefat_offset(disk)}"
    subprocess.run(["mmd", "-i", fat, "::halatha"], check=True)
    for src, dest in (
        (release / "9pchalatha", "::9pchal"),
        (release / "tlsclient.386", "::halatha/tlsclient"),
        (outdir / "trust", "::halatha/trust"),
        (outdir / "wrong-trust", "::halatha/wrong-trust"),
        (ini, "::plan9.ini"),
    ):
        subprocess.run(["mcopy", "-o", "-i", fat, str(src), dest], check=True)
    return disk


def server(certdir, ready, stop, errors):
    context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    context.minimum_version = ssl.TLSVersion.TLSv1_3
    context.maximum_version = ssl.TLSVersion.TLSv1_3
    context.load_cert_chain(certdir / "server.crt", certdir / "server.key")
    try:
        with socket.socket() as listener:
            listener.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
            listener.bind(("127.0.0.1", 16443))
            listener.listen(5)
            listener.settimeout(0.5)
            ready.set()
            while not stop.is_set():
                try:
                    raw, _ = listener.accept()
                except socket.timeout:
                    continue
                try:
                    with context.wrap_socket(raw, server_side=True) as conn:
                        conn.settimeout(10)
                        if conn.recv(4096):
                            conn.sendall(b"TLS13 NATIVE PASS\n")
                except (OSError, ssl.SSLError):
                    raw.close()
    except Exception as error:
        errors.append(error)
        ready.set()


def main():
    if len(sys.argv) != 4:
        sys.exit("usage: test-tls13-native-qemu.py base.img release-dir outdir")
    base, release, outdir = (Path(arg).resolve() for arg in sys.argv[1:])
    disk = stage(base, release, outdir)
    logpath = outdir / "serial.log"
    ready = threading.Event()
    stop = threading.Event()
    errors = []
    thread = threading.Thread(target=server, args=(outdir, ready, stop, errors), daemon=True)
    thread.start()
    if not ready.wait(5) or errors:
        raise RuntimeError(f"TLS server failed: {errors}")
    qemu = [
        "qemu-system-i386", "-m", "1024", "-display", "none",
        "-monitor", "none", "-serial", "stdio", "-no-reboot",
        "-netdev", "user,id=n0",
        "-device", "ne2k_pci,netdev=n0,mac=52:54:00:44:13:04",
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

            def run(command, timeout=60):
                time.sleep(0.5)
                con.buf = ""
                log.flush()
                start = logpath.stat().st_size
                con.send(command)
                if con.expect(["term% "], timeout) is None:
                    raise RuntimeError(f"timed out: {command}")
                log.flush()
                return logpath.read_bytes()[start:].decode(errors="replace")

            run("9fs 9fat", 30)
            run("ip/ipconfig ether /net/ether0 10.0.2.15 255.255.255.0 10.0.2.2", 30)
            run("cp /n/9fat/halatha/tlsclient /tmp/tlsclient", 30)
            run("chmod +x /tmp/tlsclient", 30)
            good = run("echo hello | /tmp/tlsclient -3 -n tls13-vm.local -t /n/9fat/halatha/trust tcp!10.0.2.2!16443", 60)
            if "TLS13 NATIVE PASS" not in good:
                raise RuntimeError(f"verified TLS 1.3 client failed: {good[-1400:]}")
            print("TLS13 NATIVE VERIFIED PASS", flush=True)
            bad = run("echo hello | /tmp/tlsclient -3 -n tls13-vm.local -t /n/9fat/halatha/wrong-trust tcp!10.0.2.2!16443", 60)
            if "TLS13 NATIVE PASS" in bad or "sha256=" not in bad:
                raise RuntimeError(f"wrong certificate was accepted: {bad[-1400:]}")
            print("TLS13 NATIVE WRONG CERT REJECTED", flush=True)
            if errors:
                raise RuntimeError(f"TLS server failed: {errors}")
        finally:
            proc.kill()
            proc.wait()
            stop.set()
            thread.join(2)


if __name__ == "__main__":
    main()
