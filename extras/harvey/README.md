# Harvey on Halatha/v64

Halatha's current guest is 9front on a 32-bit v86 CPU. Harvey is a long-mode
x86_64 kernel, so the browser path is **v64** (the x86_64-capable v86 fork),
not the old v86 runtime. This directory starts the replacement; it does not
keep 9front as a fallback.

## Current milestone

The v64 page boots **Aspera** on Harvey and presents its 1024x768 frame in the
browser canvas:

```
Halatha / Aspera on Harvey v64
halatha: starting /bin/aspera-live
ASPERA-LIVE READY
```

The guest writes frames through the writable `/build/aspera-live/dev/` files
on the 9P root. `js9p2000.js` accepts `Twrite` for `screen0`/`screen1` and
`ctl`, converts XRGB to canvas RGBA, and paints on `commit`.

The shell remains available with:

```sh
HARVEY_REPO=../harvey python3 extras/harvey/mkroot.py --init halatha --out local/harvey
```

## Current limits

- The Aspera shelf/note/input paths are disabled in this first display path;
  the desktop renders, but mouse/keyboard records and the shelf service still
  need wiring.
- The 9P root is writable only for the `build/aspera-live/dev` files.
- Storage and virtio-net are still read-only/experimental.

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
