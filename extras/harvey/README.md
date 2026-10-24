# Harvey on Halatha/v64

Halatha's current guest is 9front on a 32-bit v86 CPU. Harvey is a long-mode
x86_64 kernel, so the browser path is **v64** (the x86_64-capable v86 fork),
not the old v86 runtime. This directory starts the replacement; it does not
keep 9front as a fallback.

## Current milestone

The v64 page boots the **Halatha shell userland** on Harvey:

```
Halatha / Harvey on v64
halatha: starting /bin/rc
% echo HALATHA-SHELL
HALATHA-SHELL
%
```

The root is served by `extras/harvey/js9p2000.js`; v64's built-in basefs is
9P2000.L, but Harvey's `#9` is 9P2000, so the raw virtio-9p handler is used.
The boot path is:

- Harvey kernel: x86_64, ACPI, PCI, `#I`, `#l0`, `#9`.
- read-only 9P2000 root over virtio-9p.
- `boot/halatha_init.o9` binds `/dev`, `/proc`, `/srv`, `/env`.
- `/bin/rc` reads `/rc/lib/rcmain` and runs the serial shell.

`mkroot.py` stages `rc`, `echo`, `test`, `ls`, `cat` and `rcmain` into the root.
The page provides a serial input line for the shell.

## Remaining

Writable storage, the virtio-net link, and the graphical Halatha/Aspera layer.
The shell path is the first product userland; it does not use 9front.

## Build a diagnostic root

From the Halatha repository:

```sh
HARVEY_REPO=../harvey python3 extras/harvey/mkroot.py
```

Then serve the repository and open:

```
http://127.0.0.1:7070/examples/basic-v64-harvey.html
```

Run the smoke test:

```sh
node dev/test-harvey-v64-boot.mjs
```

The test intentionally stops at the kernel milestone. It becomes a full boot
test when the legacy virtio-9p path mounts the root and Harvey reaches its
init/`/net` setup.

## Runtime

The `v64/` tree is pinned from the x86_64 v86 fork. It contains the browser
runtime, the Bochs VGA plugin, the wasm CPU, SeaBIOS and the VGA BIOS. The
license is in `v64/LICENSE`.

## Next steps

1. Add legacy virtio 9P to Harvey, so `#9/0` mounts the read-only v64 root.
2. Add legacy virtio-net, so `#l0` works with v64's `virtio` network device.
3. Replace the diagnostic root with a Harvey/Halatha userland image.
4. Route the Halatha screen, terminal, hostfs and `.coin` services through
   Harvey's 9P/`#~` paths.
5. Remove the 9front image and `extras/9front` from the product path.
