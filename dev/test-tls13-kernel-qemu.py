#!/usr/bin/env python3
"""Test the TLS 1.3 kernel record layer between two isolated 9front VMs.

Usage: test-tls13-kernel-qemu.py base.img kernel outdir
The source image is copied and never modified. There is no external network.
"""

import importlib.util
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


def stage(base, kernel, outdir):
    if outdir.exists():
        raise RuntimeError(f"{outdir} exists; refusing to overwrite")
    outdir.mkdir(parents=True)
    ini = outdir / "plan9.ini"
    ini.write_text(PLAN9_INI)
    for name in ("a", "b"):
        disk = outdir / f"{name}.img"
        subprocess.run(["cp", "--sparse=always", str(base), str(disk)], check=True)
        fat = f"{disk}@@{builder.ninefat_offset(disk)}"
        subprocess.run(["mmd", "-i", fat, "::halatha"], check=True)
        for src, dest in (
            (kernel, "::9pchal"),
            (ini, "::plan9.ini"),
            (REPO / "dev/test-tls13-kernel.c", "::halatha/tls13test.c"),
        ):
            subprocess.run(["mcopy", "-o", "-i", fat, str(src), dest], check=True)


def main():
    if len(sys.argv) != 4:
        sys.exit("usage: test-tls13-kernel-qemu.py base.img kernel outdir")
    base = Path(sys.argv[1]).resolve()
    kernel = Path(sys.argv[2]).resolve()
    pair = Path(sys.argv[3]).resolve()
    stage(base, kernel, pair)
    netdev = "mcast=230.0.0.1:23458"
    a = il_pair.VM("a", pair, netdev)
    b = il_pair.VM("b", pair, netdev)
    try:
        a.boot()
        b.boot()
        for vm, address in ((a, "10.44.0.1"), (b, "10.44.0.2")):
            vm.run("9fs 9fat", 30)
            vm.run(f"ip/ipconfig ether /net/ether0 {address} 255.255.255.0", 30)
            vm.run("cp /n/9fat/halatha/tls13test.c /tmp/tls13test.c", 30)
            vm.run("8c -o /tmp/tls13test.8 /tmp/tls13test.c", 60)
            vm.run("8l -o /tmp/tls13test /tmp/tls13test.8", 60)
        b.run("ip/ping -n 1 10.44.0.1", 30, "ttl")
        a.run("/tmp/tls13test server >/tmp/tls13-server.log >[2=1] &", 30)
        for _ in range(10):
            output = a.run("cat /tmp/tls13-server.log", 30)
            if "TLS13 SERVER READY" in output:
                break
            time.sleep(1)
        else:
            raise RuntimeError(f"server did not start: {output}")
        b.run("/tmp/tls13test client", 90, "TLS13 KERNEL CLIENT PASS")
        a.run("cat /tmp/tls13-server.log", 30, "TLS13 KERNEL SERVER PASS")
        print("TLS13 KERNEL TWO VM PASS", flush=True)

        a.run("/tmp/tls13test serverbad >/tmp/tls13-serverbad.log >[2=1] &", 30)
        for _ in range(10):
            output = a.run("cat /tmp/tls13-serverbad.log", 30)
            if "TLS13 SERVER READY" in output:
                break
            time.sleep(1)
        else:
            raise RuntimeError(f"bad-key server did not start: {output}")
        b.run("/tmp/tls13test clientbad", 30, "TLS13 BAD TAG SENT")
        a.run("cat /tmp/tls13-serverbad.log", 30, "TLS13 BAD TAG REJECTED")
        print("TLS13 KERNEL BAD TAG REJECTED", flush=True)
    finally:
        a.stop()
        b.stop()


if __name__ == "__main__":
    main()
