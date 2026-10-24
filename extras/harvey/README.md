# Harvey on Halatha/v64

Halatha's current guest is 9front on a 32-bit v86 CPU. Harvey is a long-mode
x86_64 kernel, so the browser path is **v64** (the x86_64-capable v86 fork),
not the old v86 runtime. This directory starts the replacement; it does not
keep 9front as a fallback.

## Current milestone

`examples/basic-v64-harvey.html` boots the real Harvey init under v64 with the
x86_64 CPU profile and ACPI. The kernel brings up `#I`, `#l0`, virtio-9p and
the in-kernel scheduler, mounts a read-only 9P root through a small 9P2000
server, and executes `/boot/boot` from that root. The serial console reaches:

```
#9/0: virtio9p tag "host9p" nslot 16 msize 65560
#l0: virtio: 1000Mbps ...
init: mounting #9/0 to /...
...
Phase 3 Self-Test Finished
o9-supervisor: steady-state loop active (pid 1 staying alive)
```

The 9P root is served by `extras/harvey/js9p2000.js`. v64's built-in basefs
server is 9P2000.L, which Harvey does not speak; the raw virtio-9p handler
with this server is 9P2000 end to end.

The init's idle path deliberately uses one non-returning task. Repeated
spawn/free of short-lived result channels exposes a v64 use-after-free in
`alt()`, where the channel has already been freed and `qentry` is null.

The next work is the Halatha/Aspera userland and writable storage. Replace
`/boot/boot` with the product init, then remove the 9front image from the
product path.

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
