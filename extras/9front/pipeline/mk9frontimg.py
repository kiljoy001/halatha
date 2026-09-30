#!/usr/bin/env python3
"""
mk9frontimg.py - assemble a halatha 9front image for the browser.

  extras/9front/pipeline/mk9frontimg.py kernel [base.img] [out.img]

Starts from a sparse copy of an installed 9front disk (default
local/9front-halatha.img; never written), then:
  - puts the halatha kernel (9pchalatha, from kernel/build-kernel.py) in 9fat,
  - boots the copy once in QEMU on a serial console to install the guest
    files that live on the root filesystem (termrc.local),
  - writes the final plan9.ini: the halatha kernel, no boot prompts.

The result boots unattended in v86 and mounts the host over virtio-9p.
"""
import importlib.util
import subprocess
import sys
import time
from pathlib import Path

HERE = Path(__file__).resolve().parent
REPO = HERE.parent.parent.parent

# Reuse build-kernel.py's 9fat lookup, mtools wrapper and serial console.
_spec = importlib.util.spec_from_file_location("buildkernel", HERE.parent / "kernel/build-kernel.py")
bk = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(bk)

INSTALL = {"termrc.local": HERE.parent / "termrc.local",
           "install-image.rc": HERE / "install-image.rc"}

INSTALL_PLAN9INI = bk.BUILD_PLAN9INI

# 9bootfat looks files up by their 8.3 names only (boot/pc/fat.c dirname
# skips long-name entries), so the kernel's 9fat name must fit in 8.3.
KERNEL_9FAT = "9pchal"
assert len(KERNEL_9FAT) <= 8 and "." not in KERNEL_9FAT

FINAL_PLAN9INI = f"""bootfile={KERNEL_9FAT}
bootargs=local!/dev/sdC0/fs -A
nobootprompt=local!/dev/sdC0/fs -A
user=glenda
mouseport=ps2
monitor=vesa
vgasize=1024x768x16
tiltscreen=none
"""


def main():
    if len(sys.argv) < 2:
        bk.fail("usage: mk9frontimg.py kernel [base.img] [out.img]")
    kernel = Path(sys.argv[1])
    base = Path(sys.argv[2] if len(sys.argv) > 2 else REPO / "local/9front-halatha.img")
    out = Path(sys.argv[3] if len(sys.argv) > 3 else REPO / "local/9front-halatha-v9p.img")
    if not kernel.is_file():
        bk.fail(f"{kernel}: no such kernel")
    if not base.is_file():
        bk.fail(f"{base}: no such image")
    if out.exists():
        bk.fail(f"{out} exists; refusing to overwrite")

    print(f"mk9frontimg: sparse copy {base} -> {out}")
    subprocess.run(["cp", "--sparse=always", str(base), str(out)], check=True)
    fat = f"{out}@@{bk.ninefat_offset(out)}"

    stage = out.with_suffix(".plan9.ini")
    stage.write_text(INSTALL_PLAN9INI)
    bk.mtools("mcopy", "-o", "-i", fat, str(stage), "::plan9.ini")
    bk.mtools("mcopy", "-o", "-i", fat, str(kernel), "::" + KERNEL_9FAT)
    bk.mtools("mmd", "-i", fat, "::halatha")
    for name, src in INSTALL.items():
        bk.mtools("mcopy", "-i", fat, str(src), "::halatha/" + name)

    log_path = out.with_suffix(".install.log")
    qemu = ["qemu-system-i386", "-enable-kvm", "-m", "1024", "-display", "none",
            "-monitor", "none", "-serial", "stdio", "-net", "none", "-no-reboot",
            "-drive", f"file={out},format=raw,if=ide,index=0"]
    result = None
    with open(log_path, "w") as log:
        proc = subprocess.Popen(qemu, stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                                stderr=subprocess.STDOUT)
        con = bk.Console(proc, log)
        try:
            for n in (1, 2):
                if con.expect(["init: starting /bin/rc"], 300) is None:
                    bk.fail(f"no rc on the console (start {n}); see {log_path}")
            time.sleep(3)
            con.send("9fs 9fat")
            time.sleep(2)
            con.send("rc /n/9fat/halatha/install-image.rc")
            result = con.expect(["INSTALL-OK", "INSTALL-FAIL"], 300)
            con.send("fshalt")
            con.expect(["done halting", "halted"], 120)
        finally:
            proc.kill()
            proc.wait()
    if result != "INSTALL-OK":
        bk.fail(f"install did not succeed ({result}); see {log_path}")

    stage.write_text(FINAL_PLAN9INI)
    bk.mtools("mcopy", "-o", "-i", fat, str(stage), "::plan9.ini")
    bk.mtools("mdeltree", "-i", fat, "::halatha")
    stage.unlink()
    print(f"mk9frontimg: {out} ready ({out.stat().st_size} bytes, boots 9pchalatha unattended)")


if __name__ == "__main__":
    main()
