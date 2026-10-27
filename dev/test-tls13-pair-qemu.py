#!/usr/bin/env python3
"""Test TLS 1.3 between two isolated 9front VMs over a QEMU socket LAN."""

import importlib.util
import os
import subprocess
import sys
import time
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent
spec = importlib.util.spec_from_file_location(
    "kernel_builder", REPO / "extras/9front/kernel/build-kernel.py"
)
builder = importlib.util.module_from_spec(spec)
spec.loader.exec_module(builder)
pair_spec = importlib.util.spec_from_file_location(
    "il_pair", REPO / "dev/test-il-ygg-qemu.py"
)
il_pair = importlib.util.module_from_spec(pair_spec)
pair_spec.loader.exec_module(il_pair)

PLAN9_INI = """bootfile=9pchal
bootargs=local!/dev/sdC0/fs -A
nobootprompt=local!/dev/sdC0/fs -A
user=glenda
console=0 b115200
mouseport=ps2
"""


def stage(base, outdir):
    if outdir.exists():
        raise RuntimeError(f"{outdir} exists; refusing to overwrite")
    outdir.mkdir(parents=True)
    env = dict(os.environ, GOOS="plan9", GOARCH="386", CGO_ENABLED="0")
    proxy = outdir / "tls13proxy"
    server = outdir / "tls13server"
    subprocess.run(["go", "build", "-o", str(proxy), "./extras/9front/tls13proxy"],
                   cwd=REPO, env=env, check=True)
    subprocess.run(["go", "build", "-o", str(server), "./dev/tls13-vm-server"],
                   cwd=REPO, env=env, check=True)
    subprocess.run([
        "openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes",
        "-keyout", str(outdir / "server.key"),
        "-out", str(outdir / "server.crt"), "-days", "1",
        "-subj", "/CN=tls13-vm.local",
        "-addext", "subjectAltName=DNS:tls13-vm.local",
        "-addext", "basicConstraints=critical,CA:TRUE",
    ], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, check=True)
    ini = outdir / "plan9.ini"
    ini.write_text(PLAN9_INI)
    for name, binary in (("a", server), ("b", proxy)):
        disk = outdir / f"{name}.img"
        subprocess.run(["cp", "--sparse=always", str(base), str(disk)], check=True)
        fat = f"{disk}@@{builder.ninefat_offset(disk)}"
        subprocess.run(["mmd", "-i", fat, "::halatha"], check=True)
        files = [(binary, f"::halatha/tls13{name}"),
                 (outdir / "server.crt", "::halatha/server.crt"),
                 (ini, "::plan9.ini")]
        if name == "a":
            files.append((outdir / "server.key", "::halatha/server.key"))
        for src, dest in files:
            subprocess.run(["mcopy", "-o", "-i", fat, str(src), dest], check=True)


def main():
    if len(sys.argv) != 3:
        sys.exit("usage: test-tls13-pair-qemu.py base.img outdir")
    base = Path(sys.argv[1]).resolve()
    pair = Path(sys.argv[2]).resolve()
    stage(base, pair)
    netdev = "mcast=230.0.0.1:23457"
    a = il_pair.VM("a", pair, netdev)
    b = il_pair.VM("b", pair, netdev)
    try:
        a.boot()
        b.boot()
        for vm, address in ((a, "10.44.0.1"), (b, "10.44.0.2")):
            vm.run("9fs 9fat", 30)
            vm.run("ip/ipconfig loopback /dev/null 127.1 /128", 30)
            vm.run(f"ip/ipconfig ether /net/ether0 {address} 255.255.255.0", 30)
            vm.run(f"cp /n/9fat/halatha/tls13{vm.name} /tmp/tls13{vm.name}", 30)
            vm.run(f"chmod +x /tmp/tls13{vm.name}", 30)
        b.run("ip/ping -n 1 10.44.0.1", 30, "ttl")
        print("VM PAIR LINK PASS", flush=True)
        a.run("/tmp/tls13a >/tmp/tls13server.log >[2=1] &", 30)
        for _ in range(10):
            output = a.run("cat /tmp/tls13server.log", 30)
            if "TLS13 PEER LISTEN" in output:
                break
            time.sleep(1)
        else:
            raise RuntimeError(f"TLS server did not listen: {output}")
        b.run("/tmp/tls13b -once -listen 127.0.0.1:14443 -target 10.44.0.1:16443 -servername tls13-vm.local -ca /n/9fat/halatha/server.crt >/tmp/tls13proxy.log >[2=1] &", 30)
        b.run("hget http://127.0.0.1:14443/", 30, "VM PEER TLS13 PASS")
        b.run("cat /tmp/tls13proxy.log", 30, "TLS13 CONNECTED")
        print("TLS13 TWO VM PASS", flush=True)
    finally:
        a.stop()
        b.stop()


if __name__ == "__main__":
    main()
