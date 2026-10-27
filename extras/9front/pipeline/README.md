# 9front Image Pipeline

This pipeline treats a live 9front/QEMU system as the image authoring
environment and v86 as the browser runtime. The first useful target is a
terminal-only browser node that joins Yggdrasil and connects to an existing
Plan 9 auth/cpu/file grid; o9c and application state can live on the grid.

1. Create a raw target disk:

   ```sh
   extras/9front/pipeline/mk9frontdisk.sh local/9front-halatha.img 2G
   ```

2. Build the Halatha payload ISO:

   ```sh
   extras/9front/pipeline/mk9frontpayload.sh local/halatha-9front-payload.iso
   ```

   To include Yggdrasil binaries:

   ```sh
   YGG_BIN=/path/to/386/yggdrasil \
   YGGCTL_BIN=/path/to/386/yggdrasilctl \
   extras/9front/pipeline/mk9frontpayload.sh local/halatha-9front-payload.iso
   ```

3. Boot QEMU with the 9front installer ISO, the blank raw disk, and the payload:

   ```sh
   extras/9front/pipeline/run9front-qemu.sh local/9front-halatha.img
   ```

   This defaults to `BOOT=d`, so QEMU boots the 9front installer CD.

4. Install 9front onto `local/9front-halatha.img`.

5. Boot the installed disk in QEMU with the payload still attached, mount the
   payload ISO, then run:

   ```sh
   BOOT=c extras/9front/pipeline/run9front-qemu.sh local/9front-halatha.img
   ```

   ```rc
   rc /mnt/cd/install/install-halatha.rc
   ```

   or:

   ```rc
   rc /n/cd/install/install-halatha.rc
   ```

   For the terminal-only grid image shape, also run:

   ```rc
   rc /mnt/cd/install/halatha-grid-terminal.rc
   ```

   or:

   ```rc
   rc /n/cd/install/halatha-grid-terminal.rc
   ```

6. Serve the resulting raw disk to v86:

   ```text
   /examples/basic-v86-9front-direct.html?iso=/local/9front-halatha.img&size=<bytes>
   ```

   For the default 2 GiB disk created above, `<bytes>` is `2147483648`.

7. Boot with real TCP through WISP when testing Yggdrasil/grid connections:

   ```text
   /examples/basic-v86-9front-direct.html?iso=/local/9front-halatha.img&size=<bytes>&relay=wisp://your-vps:7655
   ```

## Grid Terminal Smoke

After booting the customized image:

```rc
cat /tmp/halatha-net.log
cat /tmp/ygg-start.log
yggdrasilctl getSelf
```

Set the grid dial strings and connect:

```rc
HALATHA_CPU=tcp!<ygg-cpu-address>!17010
HALATHA_FS=tcp!<ygg-file-address>!564
grid
```

If you prefer not to bake addresses into the image, leave them as runtime env
vars. For a production image, put site-specific values in `/lib/ndb/local` or a
small `/cfg/halatha/grid.rc` script during post-install.

## Browser guest image

The installer disk above is the base for the browser image. Build the
`9pchalatha` kernel with virtio-9p and the WebUSB software controller, then
install the boot hook and profile scripts into a new disk:

```sh
python3 extras/9front/kernel/build-kernel.py local/9front-halatha.img local/kernel-build
python3 extras/9front/pipeline/mk9frontimg.py \
  local/kernel-build/9pchalatha local/9front-halatha.img \
  local/9front-halatha-v9p12.img
```

To include the local IPv6 IL congestion-control changes and the matching
connection server (needed for `srv -m il!<IPv6>!<port>`), build from a
9front source checkout:

```sh
NINEFRONT_SRC=/path/to/9front
NINEFRONT_IL_SOURCE="$NINEFRONT_SRC/sys/src/9/ip/il.c" \
NINEFRONT_CS_SOURCE="$NINEFRONT_SRC/sys/src/cmd/ndb/cs.c" \
python3 extras/9front/kernel/build-kernel.py \
  local/9front-halatha.img local/kernel-il-build
python3 extras/9front/pipeline/mk9frontimg.py \
  local/kernel-il-build/9pchalatha local/9front-halatha.img \
  local/9front-halatha-il.img
```

The kernel builder also emits `cs.386`; the image builder installs it when
it is beside the kernel. For a Yggdrasil interface at MTU 1500, use
`exportfs -m 1400` to keep 9P messages below the IL datagram size that fits
without IPv6 fragmentation.

The default direct page boots this image and exposes the same browser
namespace services as the 9legacy page. It serves the current rc scripts
from `/n/host/boot` and publishes `boot/seed-ready` when they are available.
The image builder installs `halctl` from `local/halctl.386`; set `HALCTL_BIN`
to use another build.
Smoke test inside 9front:

```rc
mkdir -p /n/host
ls /n/host
cat /n/host/boot/seed-ready
cat /n/host/boot/host9p
```

The direct page can still boot another image with `?iso=/path&size=<bytes>`.
