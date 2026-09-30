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

## Host 9P

The browser page now exposes a virtio-9p host namespace when `host9p` is not
`0`. The guest side still needs kernel support for `devvirtio9p` (`#9`). Smoke
test inside 9front:

```rc
mkdir -p /n/host
mount -c '#9/host9p' /n/host
ls /n/host
```

If that device is not present, the next milestone is either:

- rebuild the 9front kernel with the `devvirtio9p` driver used by the 9legacy
  image, or
- bake the existing `serial9p` fallback into the installed 9front image.
