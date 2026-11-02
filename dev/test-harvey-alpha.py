#!/usr/bin/env python3
"""Check Harvey's packed alpha arithmetic against scalar channel rounding."""
import argparse
from pathlib import Path
import subprocess
import tempfile

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("--harvey-repo", type=Path,
                    default=Path(__file__).resolve().parents[2] / "harvey")
args = parser.parse_args()
source = (args.harvey_repo / "sys/src/libmemdraw/draw.c").read_text()
names = {"MASK13", "MASK02", "MUL13", "MUL02", "MUL0123"}
macros = [line for line in source.splitlines()
          if line.startswith("#define ") and line.split()[1].split("(")[0] in names]
assert len(macros) == len(names), "Harvey alpha macro definitions changed"
program = "#include <stdint.h>\n#include <stdio.h>\n" + "\n".join(macros) + r'''
int main(void) {
    uint32_t a, hi, lo, s, t, expected, x, got;
    for (a = 0; a < 256; a++)
    for (hi = 0; hi < 256; hi++)
    for (lo = 0; lo < 256; lo++) {
        expected = (((a * hi + 127) / 255) << 16) | ((a * lo + 127) / 255);
        x = (hi << 16) | lo;
        got = MUL02(a, x, t);
        if (got != expected) goto fail;
        x <<= 8;
        got = MUL13(a, x, t);
        if (got != expected) goto fail;
        x |= (hi << 16) | lo;
        expected |= expected << 8;
        got = MUL0123(a, x, s, t);
        if (got != expected) goto fail;
    }
    puts("HARVEY-ALPHA PASS: 16,777,216 channel pairs at all alpha values");
    return 0;
fail:
    fprintf(stderr, "alpha=%u hi=%u lo=%u: got %08x expected %08x\n",
            a, hi, lo, got, expected);
    return 1;
}
'''
with tempfile.TemporaryDirectory(prefix="harvey-alpha-") as directory:
    root = Path(directory)
    (root / "check.c").write_text(program)
    subprocess.run(["cc", "-O2", "-Wall", "-Werror", str(root / "check.c"),
                    "-o", str(root / "check")], check=True)
    subprocess.run([str(root / "check")], check=True)
