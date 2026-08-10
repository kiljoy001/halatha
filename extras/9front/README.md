# 9front guest extras — host namespace bridge

These files make a 9front (386) guest, booted under wanix/v86, mount the host
namespace at `/n/host` over the original **9P2000** protocol.

## How it fits together

The wanix host side is already in the repo:

- `fs/p9p` — a 9P2000 server over a wanix `fs.FS` (serves the namespace).
- `wasm/wasm.go` `_open9P2000` — starts `p9p.Serve` over a MessagePort for a
  task's namespace.
- `web/worker/worker.go` — hands the worker that port as `worker.p9p`.
- `v86/main.go` (disk-boot path) — relays `serial1` to `worker.p9p`: 9legacy's
  9P request frames on COM2 (`/dev/eia1`) → the kernel 9P2000 server; responses
  → serial1.

COM1 may carry boot/debug output in browser boots, so the Halatha image uses
COM2 for a clean 9P bridge.

This directory provides the 9front side:

- `serial9p.c` — opens `/dev/eia0`, posts it as `/srv/host` (inlines lib9p's
  `postfd`, libc only), and stays alive holding the fd + `/srv` entry open.
  The kernel then `mount -c /srv/host /n/host` and speaks 9P over the serial fd.
- `mkfile` — build `serial9p` on 9front (386): `mk`.
- `termrc.local` — run at boot: launch `serial9p`, then mount `/n/host`.

## Remastering the 9front image

The Phase-1 example boots the upstream 9front 386 ISO as-is (VGA/rio, no host
mount). To add the bridge, the guest image needs `serial9p` and `termrc.local`
in its root filesystem. This requires a 9front build environment and can't be
fully automated from Linux/Docker here; the steps are:

1. Boot a 9front 386 instance (in v86 or QEMU) from the upstream ISO.
2. Copy `serial9p.c` + `mkfile` into it and run `mk`, then
   `cp serial9p /rc/bin/serial9p` (or `/bin/serial9p`).
3. Install `termrc.local` as `/rc/bin/termrc.local` (termrc sources it if it
   exists; if not, append the contents to the end of `/rc/bin/termrc`).
4. Repack the modified root into a disk image and serve that as the wanix VM's
   `hda` instead of the raw ISO.

   The simplest route: run a proper `inst/begin` install to a fresh `hda`
   image, then customize the installed system (step 2–3) and use that image.
   `mk`-based ISO remastering (the dist tree under `/sys/src/cmd/install`) is
   the alternative if you want a single bootable image.

5. Boot it under wanix:
   ```html
   <wanix-bind dst="#vm/v86" type="archive" src="…/v86.tgz"></wanix-bind>
   <wanix-vm hda="https://your-host/9front-wanix.img"
     acpi boot="c" mem="128M" screen start
     style="width:100%;height:100%"></wanix-vm>
   ```
   From 9front rc: `ls /n/host` should list the wanix namespace.

## Status / verification

- Host side (`fs/p9p`, kernel wiring, v86 serial relay): builds under js/wasm;
  `fs/p9p` is unit-tested against a 9P2000 client (Stat, Readdir, Open/Read,
  Create/Write/Read, Mkdir).
- 9front side (`serial9p.c`, `termrc.local`): **not built or run here** — needs
  a 9front environment and a remastered image. The `postfd` semantics are taken
  from 9front lib9p's `post.c`/`srv.c`. Verify by booting the remastered image
  and checking `ls /n/host` from 9front rc.

## 9legacy (Plan 9 from Bell Labs, 386)

9legacy is an easier target than 9front for the browser path: it's 386, v86
boots it today (copy.sh ships a 16MB `9legacy.img`), and it speaks 9P2000 — so
the same `fs/p9p` host bridge works unchanged. `serial9p.c` is **libc-only** (it
inlines the `/srv` posting instead of calling lib9p), so it compiles on 9legacy
as-is; `mk` (`mkone`) works there too.

The only 9legacy difference is the boot hook: 9front's `termrc` sources
`/rc/bin/termrc.local`, but 9legacy's may not. The image builder appends the
bridge directly to the end of `/rc/bin/termrc` (it always runs at boot):

```rc
# wanix /n/host bridge over eia1
if(test -x /bin/serial9p){
	serial9p /dev/eia1 host &
	for(i in 1 2 3 4 5){ if(! test -e /srv/host) sleep 1 }
	test -e /srv/host && mount -c /srv/host /n/host &
}
```

The copy.sh image can still emit early boot/debug text on COM1. To keep 9P
clean, pass `-serial-dev /dev/eia1` when building Halatha images and enable
v86 `uart1`. The builder also removes `console=0 b115200` from `PLAN9.INI`,
adds a short `menudefault=1280x768,1` so the browser boot does not depend on
typing at the startup menu, and `*nousbboot=1` to skip the boot-time USB post.
9legacy's local boot path can still print a non-fatal `/srv/usb` warning while
probing partitions; suppressing that completely requires a rebuilt 9legacy
bootfile.
For browser boots, the builder also rewrites `/rc/bin/termrc` so network setup
runs from `/rc/bin/wanixnet` in the background when a NIC is present. That keeps
rio startup independent from DHCP/proxy delays. `usbstart` remains opt-in via
`wanixusb=1`.

Remaster flow:

1. Build a 386 Plan 9 `serial9p` binary on a Plan 9/9front system and copy it
   to the host, for example `/tmp/wanix-serial9p.386`.
2. Patch the 16MB copy.sh image from the host:
   ```sh
   GOCACHE=/tmp/go-build-wanix go run extras/9front/mk9legacyimg.go \
     -base ~/Downloads/9legacy-copysh.img \
     -serial9p /tmp/wanix-serial9p.386 \
     -serial-dev /dev/eia1 \
     -out local/9legacy-wanix.img
   ```
3. Serve `local/9legacy-wanix.img` and boot it:
	   ```html
	   <wanix-vm hda="../local/9legacy-wanix.img"
	     boot="c" mem="512M" screen start
	     style="width:100%;height:100%"></wanix-vm>
	   ```
   From 9legacy rc: `ls /n/host` lists the wanix namespace.

### Networking 9legacy under v86 (for the grid path)

9legacy has an **ne2k** driver, not virtio. Wanix disk-boot VMs default to no
NIC; give the guest an explicit NIC + relay when networking is wanted:

```html
<wanix-vm hda="…" netdev="ne2k" relay="wisp://your-wisp-relay:port"
  boot="c" mem="512M" screen start></wanix-vm>
```

Run a WISP relay (WebSocket→TCP) on a node that can reach your grid (and, if
you go that route, Yggdrasil peers). Then the guest runs your 9front/9legacy
Yggdrasil port (build it for 386 — the open item) and reaches cpu/auth/file
servers on the mesh.
