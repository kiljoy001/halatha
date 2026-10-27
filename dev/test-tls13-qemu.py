#!/usr/bin/env python3
"""Exercise the 9front TLS 1.3 user-space bridge in an isolated QEMU VM.

Usage: test-tls13-qemu.py base.img outdir
The base image is copied and never modified. Only QEMU user networking is
attached; the test servers bind host loopback.
"""

import importlib.util
import os
import shutil
import socket
import ssl
import subprocess
import sys
import threading
import time
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent
PLAN9_INI = """bootfile=9pchal
bootargs=local!/dev/sdC0/fs -A
nobootprompt=local!/dev/sdC0/fs -A
user=glenda
console=0 b115200
mouseport=ps2
"""
spec = importlib.util.spec_from_file_location(
    "kernel_builder", REPO / "extras/9front/kernel/build-kernel.py"
)
builder = importlib.util.module_from_spec(spec)
spec.loader.exec_module(builder)


def stage(base, outdir):
    if outdir.exists():
        raise RuntimeError(f"{outdir} exists; refusing to overwrite")
    outdir.mkdir(parents=True)
    disk = outdir / "test.img"
    subprocess.run(["cp", "--sparse=always", str(base), str(disk)], check=True)
    binary = outdir / "tls13proxy"
    env = dict(os.environ, GOOS="plan9", GOARCH="386", CGO_ENABLED="0")
    subprocess.run(
        ["go", "build", "-o", str(binary), "./extras/9front/tls13proxy"],
        cwd=REPO, env=env, check=True
    )
    subprocess.run([
        "openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes",
        "-keyout", str(outdir / "server.key"),
        "-out", str(outdir / "server.crt"), "-days", "1",
        "-subj", "/CN=tls13-vm.local",
        "-addext", "subjectAltName=DNS:tls13-vm.local",
        "-addext", "basicConstraints=critical,CA:TRUE",
    ], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, check=True)
    subprocess.run([
        "openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes",
        "-keyout", str(outdir / "wrong.key"),
        "-out", str(outdir / "wrong.crt"), "-days", "1",
        "-subj", "/CN=wrong.local",
    ], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, check=True)
    subprocess.run([
        "openssl", "req", "-new", "-newkey", "rsa:2048", "-nodes",
        "-keyout", str(outdir / "client.key"),
        "-out", str(outdir / "client.csr"), "-subj", "/CN=tls13-client",
    ], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, check=True)
    (outdir / "client.ext").write_text(
        "basicConstraints=critical,CA:FALSE\n"
        "keyUsage=critical,digitalSignature\n"
        "extendedKeyUsage=clientAuth\n"
    )
    subprocess.run([
        "openssl", "x509", "-req", "-in", str(outdir / "client.csr"),
        "-CA", str(outdir / "server.crt"), "-CAkey", str(outdir / "server.key"),
        "-CAcreateserial", "-out", str(outdir / "client.crt"),
        "-days", "1", "-extfile", str(outdir / "client.ext"),
    ], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, check=True)
    plan9ini = outdir / "plan9.ini"
    plan9ini.write_text(PLAN9_INI)
    fat = f"{disk}@@{builder.ninefat_offset(disk)}"
    subprocess.run(["mmd", "-i", fat, "::halatha"], check=True)
    for src, dest in (
        (binary, "::halatha/tls13proxy"),
        (outdir / "server.crt", "::halatha/server.crt"),
        (outdir / "wrong.crt", "::halatha/wrong.crt"),
        (outdir / "client.crt", "::halatha/client.crt"),
        (outdir / "client.key", "::halatha/client.key"),
        (plan9ini, "::plan9.ini"),
    ):
        subprocess.run(["mcopy", "-o", "-i", fat, str(src), dest], check=True)
    return disk


def serve_tls(port, certdir, maximum, stop, require_client=False):
    context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    context.minimum_version = maximum
    context.maximum_version = maximum
    context.load_cert_chain(certdir / "server.crt", certdir / "server.key")
    if require_client:
        context.verify_mode = ssl.CERT_REQUIRED
        context.load_verify_locations(certdir / "server.crt")
    with socket.socket() as listener:
        listener.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        listener.bind(("127.0.0.1", port))
        listener.listen(5)
        listener.settimeout(0.5)
        while not stop.is_set():
            try:
                conn, _ = listener.accept()
            except socket.timeout:
                continue
            try:
                with context.wrap_socket(conn, server_side=True) as stream:
                    stream.settimeout(5)
                    request = stream.recv(4096)
                    body = b"X" * 262144 if b"GET /large " in request else b"VM TLS13 PASS\n"
                    stream.sendall(
                        b"HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\n"
                        + f"Content-Length: {len(body)}\r\n".encode()
                        + b"Connection: close\r\n\r\n" + body
                    )
            except (OSError, ssl.SSLError):
                conn.close()


def main():
    if len(sys.argv) != 3:
        sys.exit("usage: test-tls13-qemu.py base.img outdir")
    base = Path(sys.argv[1]).resolve()
    certdir = Path(sys.argv[2]).resolve()
    for tool in ("cp", "go", "openssl", "mmd", "mcopy", "qemu-system-i386"):
        if shutil.which(tool) is None:
            sys.exit(f"missing tool: {tool}")
    disk = stage(base, certdir)
    logpath = disk.with_name("tls13-serial.log")
    stop = threading.Event()
    servers = []
    for port, maximum, require_client in (
        (16443, ssl.TLSVersion.TLSv1_3, False),
        (16444, ssl.TLSVersion.TLSv1_2, False),
        (16445, ssl.TLSVersion.TLSv1_3, True),
    ):
        thread = threading.Thread(
            target=serve_tls,
            args=(port, certdir, maximum, stop, require_client), daemon=True
        )
        thread.start()
        servers.append(thread)
    qemu = [
        "qemu-system-i386", "-m", "1024", "-display", "none",
        "-monitor", "none", "-serial", "stdio", "-no-reboot",
        "-netdev", "user,id=n0",
        "-device", "ne2k_pci,netdev=n0,mac=52:54:00:44:13:01",
        "-drive", f"file={disk},format=raw,if=ide,index=0",
    ]
    if os.access("/dev/kvm", os.R_OK | os.W_OK):
        qemu.insert(1, "-enable-kvm")
    with logpath.open("w") as log:
        proc = subprocess.Popen(
            qemu, stdin=subprocess.PIPE, stdout=subprocess.PIPE,
            stderr=subprocess.STDOUT
        )
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
                    raise RuntimeError(f"{command} failed: {output[-1200:]}")
                return output

            run("9fs 9fat", 30)
            run("ip/ipconfig loopback /dev/null 127.1 /128", 30)
            run("ip/ipconfig ether /net/ether0 10.0.2.15 255.255.255.0 10.0.2.2", 30)
            run("ip/ping -n 1 10.0.2.2", 30, "ttl")
            run("cp /n/9fat/halatha/tls13proxy /tmp/tls13proxy", 30)
            run("chmod +x /tmp/tls13proxy", 30)
            run("/tmp/tls13proxy -once -listen 127.0.0.1:14443 -target 10.0.2.2:16443 -servername tls13-vm.local -ca /n/9fat/halatha/server.crt >/tmp/tls13-ok.log >[2=1] &", 30)
            result = run("hget http://127.0.0.1:14443/", 30, "VM TLS13 PASS")
            run("cat /tmp/tls13-ok.log", 30, "TLS13 CONNECTED")
            print("TLS13 VERIFIED PASS", flush=True)
            run("/tmp/tls13proxy -once -listen 127.0.0.1:14446 -target 10.0.2.2:16443 -servername tls13-vm.local -ca /n/9fat/halatha/server.crt >/tmp/tls13-large.log >[2=1] &", 30)
            run("hget http://127.0.0.1:14446/large >/tmp/tls13-large", 30)
            run("wc -c /tmp/tls13-large", 30, "262144")
            print("TLS13 LARGE STREAM PASS", flush=True)
            run("/tmp/tls13proxy -once -listen 127.0.0.1:14444 -target 10.0.2.2:16443 -servername tls13-vm.local -ca /n/9fat/halatha/wrong.crt >/tmp/tls13-badca.log >[2=1] &", 30)
            result = run("hget http://127.0.0.1:14444/", 30)
            if "VM TLS13 PASS" in result:
                raise RuntimeError("wrong CA was accepted")
            run("cat /tmp/tls13-badca.log", 30, "TLS13 FAIL")
            print("TLS13 WRONG CA REJECTED", flush=True)
            run("/tmp/tls13proxy -once -listen 127.0.0.1:14447 -target 10.0.2.2:16443 -servername wrong.local -ca /n/9fat/halatha/server.crt >/tmp/tls13-badname.log >[2=1] &", 30)
            result = run("hget http://127.0.0.1:14447/", 30)
            if "VM TLS13 PASS" in result:
                raise RuntimeError("wrong server name was accepted")
            run("cat /tmp/tls13-badname.log", 30, "TLS13 FAIL")
            print("TLS13 WRONG NAME REJECTED", flush=True)
            run("/tmp/tls13proxy -once -listen 127.0.0.1:14445 -target 10.0.2.2:16444 -servername tls13-vm.local -ca /n/9fat/halatha/server.crt >/tmp/tls13-old.log >[2=1] &", 30)
            result = run("hget http://127.0.0.1:14445/", 30)
            if "VM TLS13 PASS" in result:
                raise RuntimeError("TLS 1.2 server was accepted")
            run("cat /tmp/tls13-old.log", 30, "TLS13 FAIL")
            print("TLS12 DOWNGRADE REJECTED", flush=True)
            run("/tmp/tls13proxy -once -listen 127.0.0.1:14448 -target 10.0.2.2:16445 -servername tls13-vm.local -ca /n/9fat/halatha/server.crt >/tmp/tls13-no-client.log >[2=1] &", 30)
            result = run("hget http://127.0.0.1:14448/", 30)
            if "VM TLS13 PASS" in result:
                raise RuntimeError("mutual TLS server accepted a missing client certificate")
            print("TLS13 CLIENT CERT REQUIRED", flush=True)
            run("/tmp/tls13proxy -once -listen 127.0.0.1:14449 -target 10.0.2.2:16445 -servername tls13-vm.local -ca /n/9fat/halatha/server.crt -cert /n/9fat/halatha/client.crt -key /n/9fat/halatha/client.key >/tmp/tls13-mtls.log >[2=1] &", 30)
            run("hget http://127.0.0.1:14449/", 30, "VM TLS13 PASS")
            print("TLS13 MUTUAL AUTH PASS", flush=True)
        finally:
            proc.kill()
            proc.wait()
            stop.set()
            for thread in servers:
                thread.join(2)


if __name__ == "__main__":
    main()
