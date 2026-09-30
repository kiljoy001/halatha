#!/usr/bin/env python3
"""
build-kernel.py - build the halatha 9front kernel (pc + devvirtio9p) in QEMU.

  extras/9front/kernel/build-kernel.py [base.img] [outdir]

A sparse copy of an installed 9front disk image (default
local/9front-halatha.img) becomes a throwaway build machine; the base image
is never written. The copy's 9fat gets this directory's sources and a
build-only plan9.ini: the console on COM1 and no monitor, so rio cannot
start and init falls back to a plain rc on the serial console. That rc runs
build.rc; the kernel and log come back out of 9fat with mtools.

Outputs in outdir: 9pchalatha, build.log, serial.log (the whole console).
"""
import os
import select
import shutil
import subprocess
import sys
import time
from pathlib import Path

HERE = Path(__file__).resolve().parent
REPO = HERE.parent.parent.parent
SOURCES = ["devvirtio9p.c", "pchalatha", "build.rc"]

BUILD_PLAN9INI = """bootfile=9pc
bootargs=local!/dev/sdC0/fs -A
nobootprompt=local!/dev/sdC0/fs -A
user=glenda
console=0 b115200
mouseport=ps2
"""


def fail(msg):
    print("build-kernel:", msg, file=sys.stderr)
    sys.exit(1)


def ninefat_offset(img):
    """Byte offset of 9fat: the MBR's Plan 9 partition (type 0x39), then
    the 'part 9fat start end' line of the Plan 9 table in its sector 1."""
    with open(img, "rb") as f:
        mbr = f.read(512)
        if mbr[510:512] != b"\x55\xaa":
            fail(f"{img}: no MBR signature")
        start = None
        for i in range(4):
            e = mbr[446 + 16 * i: 446 + 16 * (i + 1)]
            if e[4] == 0x39:
                start = int.from_bytes(e[8:12], "little")
                break
        if start is None:
            fail(f"{img}: no Plan 9 (0x39) partition")
        f.seek((start + 1) * 512)
        table = f.read(512).split(b"\0")[0].decode("ascii", "replace")
    for line in table.splitlines():
        parts = line.split()
        if len(parts) == 4 and parts[0] == "part" and parts[1] == "9fat":
            return (start + int(parts[2])) * 512
    fail(f"{img}: no 9fat in the Plan 9 partition table")


def mtools(*args):
    subprocess.run(args, check=True)


class Console:
    """The QEMU guest's COM1 on our stdin/stdout pipes, logged in full."""

    def __init__(self, proc, log):
        self.proc, self.log, self.buf = proc, log, ""

    def expect(self, needles, timeout):
        deadline = time.time() + timeout
        while time.time() < deadline:
            for n in needles:
                if n in self.buf:
                    i = self.buf.index(n) + len(n)
                    self.buf = self.buf[i:]
                    return n
            if self.proc.poll() is not None:
                fail("QEMU exited early; see serial.log")
            r, _, _ = select.select([self.proc.stdout], [], [], 1)
            if r:
                data = os.read(self.proc.stdout.fileno(), 65536).decode("latin-1")
                self.log.write(data)
                self.log.flush()
                self.buf += data
        return None

    def send(self, line):
        self.proc.stdin.write((line + "\n").encode())
        self.proc.stdin.flush()


def main():
    base = Path(sys.argv[1] if len(sys.argv) > 1 else REPO / "local/9front-halatha.img")
    out = Path(sys.argv[2] if len(sys.argv) > 2 else REPO / "local/kernel-build")
    for tool in ("qemu-system-i386", "mcopy", "mmd"):
        if shutil.which(tool) is None:
            fail(f"{tool} not found")
    if not base.is_file():
        fail(f"{base}: no such image")
    if out.exists():
        fail(f"{out} exists; refusing to overwrite")
    out.mkdir(parents=True)

    work = out / "build.img"
    print(f"build-kernel: sparse copy {base} -> {work}")
    subprocess.run(["cp", "--sparse=always", str(base), str(work)], check=True)

    off = ninefat_offset(work)
    fat = f"{work}@@{off}"
    ini = out / "plan9.ini"
    ini.write_text(BUILD_PLAN9INI)
    mtools("mcopy", "-o", "-i", fat, str(ini), "::plan9.ini")
    mtools("mmd", "-i", fat, "::halatha")
    for name in SOURCES:
        mtools("mcopy", "-i", fat, str(HERE / name), "::halatha/" + name)
    print(f"build-kernel: 9fat at byte {off}; sources and build plan9.ini installed")

    qemu = ["qemu-system-i386", "-m", "1024", "-display", "none", "-monitor", "none",
            "-serial", "stdio", "-net", "none", "-no-reboot",
            "-drive", f"file={work},format=raw,if=ide,index=0"]
    if os.access("/dev/kvm", os.R_OK | os.W_OK):
        qemu[1:1] = ["-enable-kvm"]
    with open(out / "serial.log", "w") as log:
        proc = subprocess.Popen(qemu, stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                                stderr=subprocess.STDOUT)
        con = Console(proc, log)
        try:
            # termrc's rio fails without a display, so init restarts a plain
            # rc on the console: the second "starting /bin/rc".
            for n in (1, 2):
                if con.expect(["init: starting /bin/rc"], 300) is None:
                    fail(f"no rc on the console (start {n}); see serial.log")
            time.sleep(3)
            con.send("9fs 9fat")
            time.sleep(2)
            con.send("rc /n/9fat/halatha/build.rc")
            result = con.expect(["BUILD-OK", "BUILD-FAIL"], 1200)
            print(f"build-kernel: {result or 'TIMEOUT'}")
            con.send("fshalt")
            con.expect(["done halting", "halted"], 120)
        finally:
            proc.kill()
            proc.wait()

    mtools("mcopy", "-n", "-i", fat, "::halatha/build.log", str(out / "build.log"))
    if result == "BUILD-OK":
        mtools("mcopy", "-n", "-i", fat, "::9pchalatha", str(out / "9pchalatha"))
        print(f"build-kernel: kernel at {out / '9pchalatha'}")
    else:
        fail(f"build did not succeed; see {out / 'build.log'} and serial.log")


if __name__ == "__main__":
    main()
