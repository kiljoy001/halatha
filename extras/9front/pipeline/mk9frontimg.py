#!/usr/bin/env python3
"""
mk9frontimg.py - assemble a halatha 9front image for the browser.

  extras/9front/pipeline/mk9frontimg.py kernel [base.img] [out.img]

Starts from a sparse copy of an installed 9front disk (default
local/9front-halatha.img; never written), then:
  - puts the halatha kernel (9pchalatha, from kernel/build-kernel.py) in 9fat,
  - boots the copy once in QEMU on a serial console to install the guest
    boot hook, profile bind, and health check on the root filesystem,
  - writes the final plan9.ini: the halatha kernel, no boot prompts.

The result boots unattended in v86 and mounts the host over virtio-9p.
"""
import importlib.util
import os
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

INSTALL = {
    "termrc.local": HERE.parent / "termrc.local",
    "halatha.local": HERE.parent / "halatha.local",
    "desktop.rc": HERE.parent / "desktop.rc",
    "o9build.rc": HERE.parent / "o9build.rc",
    "bind.rc": HERE.parent / "halatha-bind.rc",
    "watch.rc": HERE.parent / "halatha-watch.rc",
    "usbboot.rc": HERE.parent / "usbboot.rc",
    "install-image.rc": HERE / "install-image.rc",
    "install-coin.rc": HERE.parent / "coin/install-coin.rc",
    "coin-up.rc": HERE.parent / "coin/coin-up.rc",
}
HALCTL = Path(os.environ.get("HALCTL_BIN", REPO / "local/halctl.386"))
NINEPMOUNT = Path(os.environ.get("NINEPMOUNT_BIN", REPO / "local/9pmount.386"))

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
nousbrc=1
"""


def main():
    if len(sys.argv) < 2:
        bk.fail("usage: mk9frontimg.py kernel [base.img] [out.img]")
    kernel = Path(sys.argv[1])
    base = Path(sys.argv[2] if len(sys.argv) > 2 else REPO / "local/9front-halatha.img")
    out = Path(sys.argv[3] if len(sys.argv) > 3 else REPO / "local/9front-halatha-v9p12.img")
    if not kernel.is_file():
        bk.fail(f"{kernel}: no such kernel")
    if not HALCTL.is_file():
        bk.fail(f"{HALCTL}: halctl missing; build it or set HALCTL_BIN")
    if not base.is_file():
        bk.fail(f"{base}: no such image")
    cs_setting = os.environ.get("CS_BIN")
    cs_binary = Path(cs_setting) if cs_setting else kernel.parent / "cs.386"
    if cs_setting and not cs_binary.is_file():
        bk.fail(f"{cs_binary}: cs binary missing")
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
    for name, src in {**INSTALL, "halctl.386": HALCTL}.items():
        bk.mtools("mcopy", "-i", fat, str(src), "::halatha/" + name)
    if NINEPMOUNT.is_file():
        bk.mtools("mcopy", "-i", fat, str(NINEPMOUNT), "::halatha/9pmount.386")
    if cs_binary.is_file():
        bk.mtools("mcopy", "-i", fat, str(cs_binary), "::halatha/cs.386")

    log_path = out.with_suffix(".install.log")
    qemu = ["qemu-system-i386", "-m", "1024", "-display", "none",
            "-monitor", "none", "-serial", "stdio", "-net", "none", "-no-reboot",
            "-drive", f"file={out},format=raw,if=ide,index=0"]
    if os.access("/dev/kvm", os.R_OK | os.W_OK):
        qemu[1:1] = ["-enable-kvm"]
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
