#!/usr/bin/env python3
"""Build Aspera's browser guest using the sibling Harvey toolchain and archives."""
import argparse
import importlib.util
from pathlib import Path
import shutil
import subprocess
import tarfile
import tempfile
from guest_compat import runtime_source, generated_source
from build_fonts import build_fonts


def main():
    here = Path(__file__).resolve().parent
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--harvey-repo", type=Path, default=here.parents[2] / "harvey")
    parser.add_argument("--out", type=Path, default=here.parents[1] / "local/aspera")
    parser.add_argument("--ttf-font", type=Path)
    args = parser.parse_args()
    repo, out = args.harvey_repo.resolve(), args.out.resolve()
    root = out / "rootfs"
    root.mkdir(parents=True, exist_ok=True)
    spec = importlib.util.spec_from_file_location("prism", repo / "util/9vm/prism.py")
    prism = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(prism)
    font_draw = build_fonts(repo, out, prism, args.ttf_font)
    # Compile with Harvey's flags and archives, keeping all build output here.
    with tempfile.TemporaryDirectory(prefix="aspera-build-", dir=out) as tmp:
        source_dir = Path(tmp) / 'source'
        shutil.copytree(repo / 'sys/src/cmd/o9aspera', source_dir)
        live = source_dir / 'live.o9'
        live_source = live.read_text()
        # The upstream display-only debugging mode must not report full readiness.
        live_source = live_source.replace('running = canvas.ready();',
            'running = canvas.ready() && note.ready() && shelfService.ready();')
        live.write_text(live_source)
        exec_obj = Path(tmp) / 'aspera_exec.o'
        subprocess.run([prism.CC, *prism.CFLAGS, str(here / 'aspera_exec.c'),
                        '-o', str(exec_obj)], check=True)
        runtime = Path(tmp) / "o9_runtime.c"
        runtime.write_text(runtime_source((Path(prism.O9_REPO) / "o9_runtime.c").read_text()))
        runtime_obj = Path(tmp) / "o9_runtime.o"
        subprocess.run([prism.CC, *prism.CFLAGS, '-DMONO_PLAN9',
                        str(runtime), '-o', str(runtime_obj)], check=True)
        welcome_resize_obj = Path(tmp) / "welcome_resize.o"
        subprocess.run([prism.CC, *prism.CFLAGS, str(here / 'welcome_resize.c'),
                        '-o', str(welcome_resize_obj)], check=True)
        for source, destination in (
            (here / "aspera_init.o9", "boot/boot"),
            (source_dir / "shelf_service.o9", "bin/aspera-shelf-service"),
            (source_dir / "welcome.o9", "bin/welcome"),
        ):
            generated, obj = Path(tmp) / "guest.c", Path(tmp) / "guest.o"
            target = root / destination
            target.parent.mkdir(parents=True, exist_ok=True)
            with generated.open("w") as output:
                subprocess.run([prism.O9C_BIN, str(source)], stdout=output, check=True)
            text = generated_source(generated.read_text())
            if source.name == 'welcome.o9':
                text = text.replace('#include <o9.h>', '#include <o9.h>\n#include <draw.h>\n#include <event.h>', 1)
            generated.write_text(text)
            shutil.copy2(generated, out / ('generated-' + source.stem + '.c'))
            subprocess.run([prism.CC, *prism.CFLAGS, '-include', str(here / 'aspera_exec.h'), f"-I{source.parent}",
                            str(generated), "-o", str(obj)], check=True)
            libs = [lib for lib in prism.LIBS if Path(lib).is_file()]
            extra = [str(welcome_resize_obj)] if source.name == 'welcome.o9' else []
            subprocess.run([prism.LD, *prism.LDFLAGS, str(obj), str(runtime_obj),
                            str(exec_obj), *extra, *libs,
                            "-o", str(target)], check=True)
            print(f"Built {destination}", flush=True)

        # Build native C Aspera Live compositor (Lola + 9front Rio)
        aspera_cmd_dir = repo / "sys/src/cmd/aspera"
        undo_obj = Path(tmp) / "undo.o"
        live_obj = Path(tmp) / "aspera_live.o"
        subprocess.run([prism.CC, *prism.CFLAGS, str(aspera_cmd_dir / "undo.c"), "-o", str(undo_obj)], check=True)
        subprocess.run([prism.CC, *prism.CFLAGS, f"-I{aspera_cmd_dir}", str(aspera_cmd_dir / "aspera_live.c"), "-o", str(live_obj)], check=True)
        subprocess.run([prism.LD, *prism.LDFLAGS, str(live_obj), str(undo_obj),
                        str(repo / "build/libthread.a"), str(repo / "build/libc.a"),
                        "-o", str(root / "bin/aspera-live")], check=True)
        print("Built bin/aspera-live (native C Lola + Rio compositor)", flush=True)

        # Build Objective-9c compiler (o9c) for the in-guest development environment
        o9c_type_obj = Path(tmp) / "o9c_type.o"
        o9c_tab_obj = Path(tmp) / "o9c_tab.o"
        o9_repo = Path(prism.O9_REPO)
        subprocess.run([prism.CC, *prism.CFLAGS, f"-I{o9_repo}/o9c",
                        str(o9_repo / "o9c/o9_type.c"), "-o", str(o9c_type_obj)], check=True)
        subprocess.run([prism.CC, *prism.CFLAGS, f"-I{o9_repo}/o9c",
                        str(o9_repo / "o9c/y.tab.c"), "-o", str(o9c_tab_obj)], check=True)
        subprocess.run([prism.LD, *prism.LDFLAGS, str(o9c_tab_obj), str(o9c_type_obj),
                        str(repo / "build/libbio.a"), str(repo / "build/libc.a"),
                        "-o", str(root / "bin/o9c")], check=True)
        print("Built bin/o9c (Objective-9c compiler)", flush=True)

        # Stage Objective-9c sample sources into guest rootfs
        (root / "sys/src").mkdir(parents=True, exist_ok=True)
        shutil.copy2(source_dir / "welcome.o9", root / "sys/src/welcome.o9")
        (root / "home").mkdir(parents=True, exist_ok=True)
        shutil.copy2(source_dir / "welcome.o9", root / "home/welcome.o9")

        # Build genuine plan9front Rio (copied into Harvey sys/src/cmd/rio)
        # as bin/rio. The Plan 9 C anonymous embedding in these sources is
        # compiled with gcc's -fplan9-extensions (the plan9port compiler),
        # not clang's -fms-extensions. Source list matches plan9front's
        # rio mkfile, plus compat.c (min/max/badrect) and frcol.c (from
        # plan9front libframe, missing in Harvey's libframe).
        rio_dir = repo / "sys/src/cmd/rio"
        rio_cflags = [f for f in prism.CFLAGS if f != "-mno-implicit-float"]
        rio_objs = []
        for name in ("rio", "data", "fsys", "line", "props", "scrl", "time",
                     "ui1", "undo", "util", "wctl", "wind", "xfid",
                     "compat", "frcol"):
            obj = Path(tmp) / f"rio-{name}.o"
            subprocess.run(["gcc", *rio_cflags, "-fplan9-extensions", "-fno-pie",
                            "-mstackrealign",
                            "-w", f"-I{rio_dir}",
                            str(rio_dir / f"{name}.c"), "-o", str(obj)], check=True)
            rio_objs.append(str(obj))
        # libcomplete compiled fresh: Harvey's complete.h gained the 9front
        # Completion.force field, so the stale archive must not be used.
        complete_obj = Path(tmp) / "rio-complete.o"
        subprocess.run([prism.CC, *prism.CFLAGS,
                        str(repo / "sys/src/libcomplete/complete.c"),
                        "-o", str(complete_obj)], check=True)
        rio_objs.append(str(complete_obj))
        # The caret fix lives in libframe too. Compile the frame sources here
        # so rebuilding Rio cannot silently retain an old libframe archive.
        for source in sorted((repo / "sys/src/libframe").glob("fr*.c")):
            obj = Path(tmp) / f"frame-{source.stem}.o"
            subprocess.run([prism.CC, *prism.CFLAGS, str(source), "-o", str(obj)], check=True)
            rio_objs.append(str(obj))
        subprocess.run([prism.LD, *prism.LDFLAGS, *rio_objs,
                        str(font_draw), str(repo / "build/libmemdraw.a"),
                        str(repo / "build/libthread.a"),
                        str(repo / "build/libplumb.a"),
                        str(repo / "build/libc.a"),
                        "-o", str(root / "bin/rio")], check=True)
        print("Built bin/rio (9front Rio window manager)", flush=True)

        subprocess.run(["python3", str(repo / "util/9vm/build_vdir.py"),
                        "--out", str(root / "bin/vdir"),
                        "--draw", str(font_draw)], check=True)

        open_obj = Path(tmp) / "rio-open.o"
        subprocess.run([prism.CC, *prism.CFLAGS, str(repo / "sys/src/cmd/rio-open.c"),
                        "-o", str(open_obj)], check=True)
        subprocess.run([prism.LD, *prism.LDFLAGS, str(open_obj),
                        str(repo / "build/libc.a"), "-o", str(root / "bin/rio-open")], check=True)

        bridge_obj = Path(tmp) / "snarfbridge.o"
        subprocess.run([prism.CC, *prism.CFLAGS, str(here / "snarfbridge.c"), "-o", str(bridge_obj)], check=True)
        subprocess.run([prism.LD, *prism.LDFLAGS, str(bridge_obj),
                        str(repo / "build/libc.a"), "-o", str(root / "bin/snarfbridge")], check=True)
        print("Built bin/snarfbridge", flush=True)
    for name in ("rc", "echo", "test", "ls", "cat", "sleep", "rm",
                 "basename", "cal", "chmod", "cleanname", "cmp", "comm",
                 "cp", "dd", "du", "mkdir", "mv", "seq", "sort", "tail",
                 "tar", "tee", "tr", "uniq", "wc",
                 # extended 9front set
                 "bind", "unmount", "sum", "crc32", "read", "pwd", "touch",
                 "ns", "strings", "factor", "primes", "dc", "lnfs",
                 "md5sum", "mount", "srv",
                 # second-tier text and inspection tools
                 "grep", "sed", "diff", "chgrp", "p", "fmt", "split", "look",
                 "ascii", "unicode", "col", "pr", "ps", "time", "xd", "mtime",
                 # crypto library known-answer self-test
                 "mctest",
                 # libtab table store: tests and command
                 "tabtest", "tabcryptest", "tab",
                 # third-tier core tools
                 "ed", "sam", "mk", "yacc", "patch",
                 "gzip", "gunzip", "zip", "unzip",
                 "iostats", "swap", "srvfs", "ramfs", "mntgen",
                 # fourth-tier core tools
                 "awk", "bzip2", "bunzip2", "plumb", "plumber",
                 # ndb: connection server, resolver, query tools
                 "cs", "csquery", "dnsquery", "ipquery", "dns",
                 "ipconfig", "ping",
                 # userspace TPM2 driver
                 "tpmfs", "tpmtest"):
        shutil.copy2(repo / f"build/aspera-{name}", root / "bin" / name)
    (root / "tmp").mkdir(parents=True, exist_ok=True)
    (root / "tmp/.keep").write_bytes(b"")  # mount point for the ram /tmp
    # Rio's window filesystem needs a scratch mount point (cexecpipe binds
    # #| at /mnt/temp) and a wsys mount point. The standalone page created
    # these in its in-memory VFS; the archive rootfs must carry them too.
    for d in ("mnt/temp", "mnt/wsys", "mnt/plumb", "n/host/clip", "net"):
        (root / d).mkdir(parents=True, exist_ok=True)
    (root / "rc/lib").mkdir(parents=True, exist_ok=True)
    shutil.copy2(repo / "rc/lib/rcmain", root / "rc/lib/rcmain")
    (root / "lib/aspera").mkdir(parents=True, exist_ok=True)
    shutil.copy2(here / "rio-start.rc", root / "lib/aspera/start.rc")
    shutil.copy2(here / "net.rc", root / "lib/aspera/net.rc")
    (root / "lib/aspera/net.rc").chmod(0o755)
    shutil.copy2(here / "plumbing", root / "lib/aspera/plumbing")
    (root / "n/host/note.txt").touch()
    (root / "lib/ndb").mkdir(parents=True, exist_ok=True)
    shutil.copy2(here / "ndb-local", root / "lib/ndb/local")
    font_dst = root / "lib/font/bit/lucm"
    font_dst.mkdir(parents=True, exist_ok=True)
    for font in (repo / "lib/font/bit/lucm").iterdir():
        if font.is_file():
            shutil.copy2(font, font_dst / font.name)
    shutil.copy2(repo / "build/harvey.32bit", out / "harvey.32bit")
    # Archive of the staged rootfs for <wanix-vm multiboot> pages: the VM's
    # 9P root is its task namespace, so the archive is bound (union) at ".".
    tarpath = out / "rootfs.tar.gz"
    with tarfile.open(tarpath, "w:gz") as tf:
        tf.add(root, arcname=".")
    print(f"Staged browser guest in {out} (rootfs archive {tarpath.name})")


if __name__ == "__main__":
    main()
