# Harvey on Halatha/v64

Halatha's current guest is 9front on a 32-bit v86 CPU. Harvey is a long-mode
x86_64 kernel, so the browser path is **v64** (the x86_64-capable v86 fork),
not the old v86 runtime. This directory starts the replacement; it does not
keep 9front as a fallback.

## Current milestone

`examples/basic-v64-harvey.html` boots the Harvey multiboot kernel on v64 with
the x86_64 CPU profile and ACPI. The serial console reaches:

```
Harvey
CPU Signature: 670
CPU Freq. 1000MHz
init: mounting #9/0 to /...
```

Harvey's built-in init then fails to open `#9/0` on v64. The v64 virtio-9p
device is legacy/transitional: it exposes the standard config/queue/ISR BARs
but its PCI capabilities are not the modern memory-BAR layout Harvey's
`#9` driver currently assumes. The next change is the legacy-virtio path for
9P (and virtio-net), not a 9front compatibility shim.

The CPUID signature `0x670` also needs a v64-safe frequency path: v64 does not
implement MSR 0x2a, so `archamd64.c` must not take a #GP while probing the
FSB. The current kernel has a narrow diagnostic fallback; it needs a clean
`rdmsr_safe`/CPUID 0x16 path before release.

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
