#!/usr/bin/env python3
"""Stage a read-only Harvey root for the v64 browser VM.

This is a diagnostic root: the Harvey kernel currently boots with its built-in
init and reaches /net loopback; it does not yet mount the v64 virtio-9p root
because Harvey's 9P driver needs the legacy virtio path. The generated tree is
still useful for boot testing and for the eventual Halatha userland image.
"""
import argparse
import os
import shutil
import subprocess
import sys
from pathlib import Path

HERE = Path(__file__).resolve().parent
TOOLS = HERE / "tools"
DIRS = ("amd64/bin", "bin", "boot", "dev", "env", "mnt", "net", "n/host", "proc", "srv")

def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--harvey-repo", default=os.environ.get("HARVEY_REPO", str(HERE.parent.parent.parent / "harvey")))
    ap.add_argument("--out", default=str(HERE.parent.parent / "local/harvey"))
    ap.add_argument("--kernel", default="")
    ap.add_argument("--init", choices=("aspera", "halatha", "full", "minimal"), default="aspera")
    args = ap.parse_args()
    repo = Path(args.harvey_repo).resolve()
    out = Path(args.out).resolve()
    root = out / "rootfs"
    base = out / "base"
    if not (repo / "README.md").is_file():
        print(f"missing Harvey repo: {repo}", file=sys.stderr)
        return 1
    kernel = Path(args.kernel) if args.kernel else repo / "build/harvey.32bit"
    if not kernel.is_file():
        print(f"missing kernel: {kernel}", file=sys.stderr)
        return 1
    if out.exists():
        shutil.rmtree(out)
    out.mkdir(parents=True)
    root.mkdir()
    for d in DIRS:
        (root / d).mkdir(parents=True, exist_ok=True)
    shutil.copy2(repo / "README.md", root / "README.md")
    if args.init == "aspera":
        boot = repo / "build/aspera-init"
        if not boot.is_file():
            subprocess.run([sys.executable, str(repo / "util/9vm/build_aspera_init.py")], check=True)
        live = repo / "build/aspera-live-bin"
        if not live.is_file():
            subprocess.run([sys.executable, str(repo / "util/9vm/prism.py"),
                            "build", str(repo / "sys/src/cmd/o9aspera/live.o9"),
                            "-o", str(live)], check=True)
        for dest, src in (
            ("bin/rc", "build/aspera-rc"),
            ("bin/echo", "build/aspera-echo"),
            ("bin/test", "build/aspera-test"),
            ("bin/ls", "build/aspera-ls"),
            ("bin/cat", "build/aspera-cat"),
            ("bin/aspera-live", "build/aspera-live-bin"),
            ("bin/aspera-shelf-service", "build/aspera-live/shelf-service"),
            ("rc/lib/rcmain", "rc/lib/rcmain"),
        ):
            (root / dest).parent.mkdir(parents=True, exist_ok=True)
            shutil.copy2(repo / src, root / dest)
        asbase = root / "build/aspera-live"
        (asbase / "dev").mkdir(parents=True, exist_ok=True)
        (asbase / "bin").mkdir(parents=True, exist_ok=True)
        (asbase / "note.txt").write_text("Hi!\n")
        shutil.copy2(repo / "build/aspera-live/shelf-service", asbase / "shelf-service")
        (asbase / "notepath").write_text("build/aspera-live/note.txt\n")
        (asbase / "clock").write_text("00:00\n")
        for name in ("ctl", "screen0", "screen1", "input"):
            (asbase / "dev" / name).write_bytes(b"")
        shutil.copy2(repo / "build/aspera-live-bin", root / "boot/boot")
    elif args.init == "halatha":
        boot = repo / "build/halatha-init"
        if not boot.is_file():
            subprocess.run([sys.executable, str(repo / "util/9vm/build_halatha_init.py")], check=True)
        for dest, src in (
            ("bin/rc", "build/aspera-rc"),
            ("bin/echo", "build/aspera-echo"),
            ("bin/test", "build/aspera-test"),
            ("bin/ls", "build/aspera-ls"),
            ("bin/cat", "build/aspera-cat"),
            ("rc/lib/rcmain", "rc/lib/rcmain"),
        ):
            (root / dest).parent.mkdir(parents=True, exist_ok=True)
            shutil.copy2(repo / src, root / dest)
    elif args.init == "full":
        boot = repo / "boot/boot"
        if not boot.is_file():
            subprocess.run([sys.executable, str(repo / "util/9vm/build_boot.py")], check=True)
    else:
        boot = repo / "build/v64init"
        if not boot.is_file():
            subprocess.run([sys.executable, str(repo / "util/9vm/build_v64init.py")], check=True)
    shutil.copy2(boot, root / "boot/boot")
    init = repo / "build/init"
    if init.is_file():
        shutil.copy2(init, root / "boot/init")
    shutil.copy2(kernel, out / "harvey.32bit")
    with (out / "fs.json").open("w") as f:
        subprocess.run([sys.executable, str(TOOLS / "fs2json.py"), "--out", "-", str(root)],
                       stdout=f, check=True)
    base.mkdir()
    subprocess.run([sys.executable, str(TOOLS / "copy-to-sha256.py"), str(root), str(base)],
                   check=True)
    print(f"staged {out}")
    print(f"  kernel {out / 'harvey.32bit'}")
    print(f"  root   {out / 'fs.json'} + {out / 'base'}")
    return 0

if __name__ == "__main__":
    raise SystemExit(main())
