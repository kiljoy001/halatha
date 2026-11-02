# Aspera on Halatha

`examples/basic-v64-aspera.html` boots Harvey's amd64 kernel in the v64 browser
CPU and launches the adapted plan9front Rio with editable title bars. The guest
renders through its draw device; the browser displays the framebuffer and
forwards mouse and keyboard input.

Build and serve the guest:

```sh
python3 extras/harvey/build_aspera.py
python3 -m http.server 7072 --bind 127.0.0.1
```

Open <http://127.0.0.1:7072/examples/basic-v64-aspera.html>. Reload after rebuilding
to boot the new binaries. Click the display to capture the mouse; Escape releases
browser pointer lock.

Drag a title bar to move the window. Ctrl+O or a double-click activates its file path; edit the
path and press Enter to open it. Ctrl+S saves the file, and Escape cancels path
editing. `/n/host/lib/aspera/note.txt` is backed by browser localStorage. Other
files under the host gateway are temporary unless explicitly backed by storage.
The guest also has a RAM-backed `/tmp`.

The guest mounts a TrueType font service at `/n/ttf` before starting Rio.
Run `ttfcheck` to render the font-port demonstration. See
[font build, validation, and limitations](FONTS.md) for stages 1–3; Rio keeps
its bitmap default until cluster-aware editing is ported.

The build requires the sibling Harvey and Objective-9C checkouts, the existing
Harvey kernel and libraries, clang, gcc with Plan 9 extensions, ld.lld, and the
staged shell/tools under Harvey's `build/aspera-*`. Pass
`--harvey-repo /path/to/harvey` if needed; `O9_REPO` selects Objective-9C.
Build output goes to this checkout's ignored `local/aspera` directory.
The crypto and table self-tests also need
`python3 util/9vm/build_libmonocypher.py`,
`python3 util/9vm/build_libtab.py` and `python3 util/9vm/build_tpm.py`
in the Harvey checkout first.

The title-bar fixes live in Harvey's `sys/src/cmd/rio/{data,rio,wind,ui1}.c`.
[Title-bar implementation notes](TITLEBARS.md) describe their behavior and the
distinction between the active Rio desktop and the separate Aspera compositors.
The build still stages the native C `/bin/aspera-live` fallback and the
Objective-9C shelf service; these are not the active Rio title-bar implementation.

The rendering fixes also require Harvey's corrected `libmemdraw/draw.c` and
`amd64/softscreen.c`. After changing those kernel sources, rebuild with
`python3 ../harvey/util/9vm/build_kernel.py` before running `build_aspera.py`.
The Aspera build compiles libframe from source to include the restored caret
drawing, rather than depending on a previously built frame archive.

Validation (requires Playwright/Chromium and the HTTP server above):

```sh
node dev/test-aspera-tools.mjs
node dev/test-rio-titlebars.mjs
node dev/test-rio-v64-boot.mjs
node dev/test-aspera-fonts.mjs
python3 dev/test-harvey-alpha.py
```

`test-aspera-tools.mjs` boots to Rio, lists `/bin`, and runs the namespace,
checksum, math and text tools through the guest shell, reading their output
back out of the guest file system.

### Guest tool set

`/bin` holds 89 binaries.  The first group is the original minimal rc set
(`rc echo test ls cat sleep rm basename cal chmod cleanname cmp comm cp dd du
mkdir mv seq sort tail tar tee tr uniq wc`).  The rest are compiled from the
sibling 9front tree by `util/9vm/build_aspera_tools.py`:

- namespace and services: `bind unmount mount srv ns`
- checksums and binary inspection: `sum crc32 md5sum xd strings read`
- text: `grep sed diff split look fmt col pr p unicode ascii tr`
- math and misc: `factor primes dc time mtime pwd touch chmod chgrp`
- crypto: `mctest` (Monocypher 4.0.2 BLAKE2b/Poly1305/X25519/EdDSA/AEAD
  known-answer vectors from Harvey's `build/libmonocypher.a`)
- tables: `tabtest`, `tabcryptest`, and `tab` (libtab config store with
  BLAKE2b/Argon2id HASHED, EdDSA SIGNED and XChaCha20-Poly1305 ENCRYPTED
  cells)
- core: `ed sam mk yacc patch gzip gunzip zip unzip iostats swap srvfs
  ramfs mntgen` (editors, the mk/yacc build tools, compression and
  namespace services)
- core: `awk bzip2 bunzip2 plumb plumber` (scripting, bzip2, and the
  plumbing client/server)
- networking: `cs dns csquery dnsquery ipquery ipconfig ping` (built,
  staged; `cs`/`dns` need `/net` and a packet link before they are useful)
- TPM: `tpmfs tpmtest` — a userspace TPM 2.0 9P driver (`/mnt/tpm`)
  with a deterministic mock backend and a raw TIS transport.

Two 9front libc/libbio extensions were required: `libbio` gained
`Blethal`/`Biofn`/`Berror` and the `errorf`/`iof`/`aux` fields, and
`libregexp.a` is now built from Harvey's sources (it was not staged with the
kernel).  `grep` is generated with plan9port `yacc`; `sed` is compiled from a
patched copy because clang does not accept its untagged anonymous union.

Deliberately still missing: commands that need 9front's newer timezone API
(`date`), `libip` (`ip/*`), `libventi` (`venti/*`), or the `page` event API.

The focused title-bar test checks actual guest keyboard/mouse input and saved
file bytes, plus canvas comparisons after Backspace, Ctrl+U, forward deletion,
PageUp/PageDown, and mouse-wheel scrolling. Use `--prompt-only` to skip the
title workflow. Screenshots go under `/tmp/rio-titlebars-*.png`. The older
`test-aspera-v64-boot.mjs` targets the previous Objective-9C canvas bridge and
is not a test of the current Rio boot.
