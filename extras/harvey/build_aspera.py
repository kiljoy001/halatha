#!/usr/bin/env python3
"""Build Aspera's browser guest using the sibling Harvey toolchain and archives."""
import argparse
import importlib.util
from pathlib import Path
import shutil
import subprocess
import tempfile
from guest_compat import runtime_source, generated_source


def main():
    here = Path(__file__).resolve().parent
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--harvey-repo", type=Path, default=here.parents[2] / "harvey")
    parser.add_argument("--out", type=Path, default=here.parents[1] / "local/aspera")
    args = parser.parse_args()
    repo, out = args.harvey_repo.resolve(), args.out.resolve()
    root = out / "rootfs"
    root.mkdir(parents=True, exist_ok=True)
    spec = importlib.util.spec_from_file_location("prism", repo / "util/9vm/prism.py")
    prism = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(prism)
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
        for source, destination in (
            (here / "aspera_init.o9", "boot/boot"),
            (source_dir / "shelf_service.o9", "bin/aspera-shelf-service"),
        ):
            generated, obj = Path(tmp) / "guest.c", Path(tmp) / "guest.o"
            target = root / destination
            target.parent.mkdir(parents=True, exist_ok=True)
            with generated.open("w") as output:
                subprocess.run([prism.O9C_BIN, str(source)], stdout=output, check=True)
            generated.write_text(generated_source(generated.read_text()))
            shutil.copy2(generated, out / ('generated-' + source.stem + '.c'))
            subprocess.run([prism.CC, *prism.CFLAGS, '-include', str(here / 'aspera_exec.h'), f"-I{source.parent}",
                            str(generated), "-o", str(obj)], check=True)
            libs = [lib for lib in prism.LIBS if Path(lib).is_file()]
            subprocess.run([prism.LD, *prism.LDFLAGS, str(obj), str(runtime_obj), str(exec_obj), *libs,
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
        subprocess.run([prism.LD, *prism.LDFLAGS, *rio_objs,
                        str(repo / "build/libdraw.a"), str(repo / "build/libmemdraw.a"),
                        str(repo / "build/libframe.a"), str(repo / "build/libthread.a"),
                        str(repo / "build/libplumb.a"),
                        str(repo / "build/libc.a"),
                        "-o", str(root / "bin/rio")], check=True)
        print("Built bin/rio (9front Rio window manager)", flush=True)

        bridge_obj = Path(tmp) / "snarfbridge.o"
        subprocess.run([prism.CC, *prism.CFLAGS, str(here / "snarfbridge.c"), "-o", str(bridge_obj)], check=True)
        subprocess.run([prism.LD, *prism.LDFLAGS, str(bridge_obj),
                        str(repo / "build/libc.a"), "-o", str(root / "bin/snarfbridge")], check=True)
        print("Built bin/snarfbridge", flush=True)
    for name in ("rc", "echo", "test", "ls", "cat", "sleep", "rm",
                 "basename", "cal", "chmod", "cleanname", "cmp", "comm",
                 "cp", "dd", "du", "mkdir", "mv", "seq", "sort", "tail",
                 "tar", "tee", "tr", "uniq", "wc"):
        shutil.copy2(repo / f"build/aspera-{name}", root / "bin" / name)
    (root / "tmp").mkdir(parents=True, exist_ok=True)
    (root / "tmp/.keep").write_bytes(b"")  # mount point for the ram /tmp
    (root / "rc/lib").mkdir(parents=True, exist_ok=True)
    shutil.copy2(repo / "rc/lib/rcmain", root / "rc/lib/rcmain")
    (root / "lib/aspera").mkdir(parents=True, exist_ok=True)
    shutil.copy2(here / "rio-start.rc", root / "lib/aspera/start.rc")
    font_dst = root / "lib/font/bit/lucm"
    font_dst.mkdir(parents=True, exist_ok=True)
    for font in (repo / "lib/font/bit/lucm").iterdir():
        if font.is_file():
            shutil.copy2(font, font_dst / font.name)
    shutil.copy2(repo / "build/harvey.32bit", out / "harvey.32bit")
    print(f"Staged browser guest in {out}")


if __name__ == "__main__":
    main()
