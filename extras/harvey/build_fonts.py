#!/usr/bin/env python3
"""Build Harvey's TTF server and matching libdraw without changing its archives."""
import argparse
import importlib.util
from pathlib import Path
import shutil
import subprocess
import tempfile
from font_fixture import colour_fixture


def build_fonts(repo, out, prism, font=None):
    out.mkdir(parents=True, exist_ok=True)
    root = out / "rootfs"
    (root / "bin").mkdir(parents=True, exist_ok=True)
    flags = [f for f in prism.CFLAGS if f != "-mno-implicit-float"]
    flags += ["-fplan9-extensions", "-mstackrealign", "-fno-pie", "-Wno-format"]
    with tempfile.TemporaryDirectory(prefix="fonts-", dir=out) as directory:
        tmp = Path(directory)

        def compile(source, name):
            obj = tmp / (name + ".o")
            subprocess.run(["gcc", *flags, str(source), "-o", str(obj)], check=True)
            return str(obj)

        for library in ("libttf", "libdraw"):
            objects = [compile(source, library + "-" + source.stem)
                       for source in sorted((repo / "sys/src" / library).glob("*.c"))
                       if source.name != "test.c"]
            if not objects:
                raise RuntimeError(f"Missing {library} sources in {repo}")
            archive = tmp / (library + ".a")
            subprocess.run(["ar", "rcs", str(archive), *objects], check=True)
            shutil.copy2(archive, out / (library + "-ttf.a"))
        libs = [str(out / "libttf-ttf.a"), str(out / "libdraw-ttf.a")]
        libs += [str(repo / "build" / ("lib" + name + ".a"))
                 for name in ("memdraw", "9p", "auth", "authsrv", "sec", "bio", "thread", "c")]
        for name, source in (
            ("truetypefs", repo / "sys/src/cmd/truetypefs.c"),
            ("ttfcheck", Path(__file__).with_name("ttfcheck.c")),
        ):
            obj = compile(source, name)
            subprocess.run([prism.LD, *prism.LDFLAGS, obj, *libs,
                            "-o", str(root / "bin" / name)], check=True)
    if font is None:
        font = Path("/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf")
    if not font.is_file():
        raise FileNotFoundError(f"Font {font} is missing; pass --ttf-font /path/to/font.ttf")
    font_dir = root / "lib/font/ttf"
    font_dir.mkdir(parents=True, exist_ok=True)
    shutil.copy2(font, font_dir / "Aspera.ttf")
    (font_dir / "ColourTest.ttf").write_bytes(colour_fixture(font.read_bytes()))
    shutil.copy2(Path(__file__).with_name("DejaVu-LICENSE"), font_dir / "DejaVu-LICENSE")
    (root / "n/ttf").mkdir(parents=True, exist_ok=True)
    (root / "n/ttf/.keep").write_bytes(b"")
    print("Built libttf, libdraw TTF hooks, truetypefs, and ttfcheck", flush=True)
    return out / "libdraw-ttf.a"


if __name__ == "__main__":
    here = Path(__file__).resolve().parent
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--harvey-repo", type=Path, default=here.parents[2] / "harvey")
    parser.add_argument("--out", type=Path, default=here.parents[1] / "local/aspera")
    parser.add_argument("--ttf-font", type=Path)
    args = parser.parse_args()
    repo = args.harvey_repo.resolve()
    spec = importlib.util.spec_from_file_location("prism", repo / "util/9vm/prism.py")
    prism = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(prism)
    build_fonts(repo, args.out.resolve(), prism, args.ttf_font)
