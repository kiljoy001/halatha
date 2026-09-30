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

The Phase-1 example boots the upstream 9front 386 ISO as-is via v86's hard-disk
path (`hda`, `boot=c`; the cdrom path reaches SeaBIOS but reports no bootable
media in this v86 build). That gives VGA/rio with no host mount. To add the
bridge, the guest image needs `serial9p` and `termrc.local` in its root
filesystem. This requires a 9front build environment and can't be fully
automated from Linux/Docker here; the steps are:

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

The repeatable authoring path is captured in
[`pipeline/README.md`](pipeline/README.md): create a raw target disk, install
9front in QEMU, attach the Halatha payload ISO, run its installer, then serve
the resulting raw disk to v86/CDN as `hda`.

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

#### Automating Yggdrasil + the TCP bridge at boot

Two pieces, in two places:

1. **Guest side — Yggdrasil daemon.** Build the image with `mk9legacyimg -ygg`
   (requires `/386/bin/yggdrasil` already in the base image). This installs
   `/rc/bin/halatha.local` (read from `extras/9front/halatha.local`) and bundles
   `extras/9front/yggdrasil.peers` as `/lib/yggdrasil.peers`. termrc already
   runs `halatha.local` in the background at boot (the `# halatha optional
   local boot hook` block). It waits for an IP interface to be bound
   (`/net/ipifc/0/ctl` — true under v86 fetch DHCP *or* QEMU SLIRP), then:
   - uses `/lib/yggdrasil.conf` if present (bundle one with
     `-add your.conf:/lib/yggdrasil.conf` for a stable node identity + your
     own peers), otherwise generates a fresh random config each boot
     (`yggdrasil -genconf` — random key, so each boot is a fresh node identity);
   - starts `/386/bin/yggdrasil -useconffile /lib/yggdrasil.conf` in the shared
     namespace so its `pkt` ip interface is visible to every process. The admin
     listener is `tcp://localhost:9001` (use `yggdrasilctl`); logs go to
     `/tmp/yggdrasil.log`;
   - **auto-injects public peers**: once the admin socket is up, it runs
     `yggdrasilctl addPeer` for every non-comment line of
     `/lib/yggdrasil.peers`. The bundled peer list is curated from the public
     [yggdrasil-network/public-peers](https://github.com/yggdrasil-network/public-peers)
     repo (IPv4-only `tcp://`/`tls://` — `quic://` is UDP and won't tunnel over
     the WISP TCP relay, IPv6 literals and `?key=` params trip rc globbing).
     Runtime peers are not persisted, but the script re-injects every boot, so
     the node joins the global Yggdrasil mesh with no manual config — a terminal
     for a 9p grid. Replace `extras/9front/yggdrasil.peers` with your own
     curated list (or bundle `/lib/yggdrasil.conf` with `Peers:` entries) to
     pin specific peers.

   `halatha.local` is a real rc file (not a Go raw-string const) precisely so
   it can use rc backtick command substitution (`{grep ...}`) to read the peers
   file — backticks would close a Go raw string.

   For day-to-day iteration, do **not** rebuild the disk image just to change
   service startup. `examples/basic-v86-9legacy-direct.html` refreshes
   `/n/host/boot/halatha.local`, `/n/host/boot/ygg-up.rc`, and
   `/n/host/boot/yggdrasil.peers` from this directory every page load. Smoke a
   changed script inside the guest with `rc /n/host/boot/ygg-up.rc`, then inspect
   `/tmp/ygg-start.log`. The disk image should be treated as a golden base and
   rebuilt only when kernel/root contents must change.

2. **Page side — the TCP bridge (WISP relay).** The relay is *not* guest-
   startable: it lives in the browser/v86 layer. v86's default `fetch` backend
   gives the guest DHCP + HTTP but **no raw outbound TCP**, so yggdrasil runs
   yet cannot dial TCP peers. To let it, launch the page with
   `?relay=wisp://your-relay:port`, or — to automate it on every boot — set
   `WISP_RELAY` near the top of `examples/basic-v86-9legacy-direct.html` to
   your relay endpoint (per-launch `?relay=` still overrides). You must run a
   WISP server on a node that can reach your Yggdrasil peers (e.g. the wisp
   relay from v86/copied.sh). With WISP in place, the guest's outbound TCP is
   routed WebSocket→real-TCP by v86, and yggdrasil dials peers normally.

So: `-ygg` makes yggdrasil come up on its own at boot **and auto-inject public
peers** so it joins the mesh; `WISP_RELAY` (or `?relay=wisp://…`) is what gives
that yggdrasil real outbound TCP to actually dial those peers. With `-ygg`
alone (default fetch backend) you get a running node with an overlay address
and peers injected into its table, but the dials can't complete until WISP
gives it a TCP path. Under QEMU user networking the peers dial for real
(no WISP needed) — that's how the build-time verify proves the mesh join.

#### objective-9c toolchain, prebuilt and installed by default

`mk9legacyimg -o9 <path>` (default `../objective-9c`) bundles the **prebuilt**
objective-9c toolchain into the image at the paths `mk install` would use, so a
booted 9legacy guest has `o9c` ready to use and develop with — no in-guest build.
This is the distribution avenue for the language: boot the VM, write `.o9`, run
`o9build`.

The prebuilt is read from `<o9dir>/release/386/` — the language's release
artifact (a 386 Plan 9 build, a consistent `o9c`+`libo9.a` pair). Keeping it
there means the prebuilt lives in the language repo (the thing being
distributed); halatha only installs it. To refresh it, rebuild on a
9legacy/9front guest (`mk install` to a prefix) and drop the result into
`release/386/`:

```
release/386/
  bin/o9c            -> /386/bin/o9c            (0775)
  lib/libo9.a        -> /386/lib/libo9.a        (0664)
  include/o9.h       -> /sys/include/o9.h       (0664)
  rc/o9build         -> /rc/bin/o9build         (0775)
  rc/o9proj          -> /rc/bin/o9proj          (0775)
  rc/o9plumb         -> /rc/bin/o9plumb         (0775)
  rc/o9plumbbuild    -> /rc/bin/o9plumbbuild    (0775)
  src/o9_dispatch.s  -> /sys/src/cmd/o9_dispatch.s   (0664)
  src/o9_runtime.c   -> /sys/src/cmd/o9_runtime.c    (0664)
  src/o9_tab_discard.c -> /sys/src/cmd/o9_tab_discard.c (0664)
  stdlib/*.o9        -> /sys/lib/o9/stdlib/*.o9 (0664)
```

The image already ships the C toolchain (`8c`/`8l`/`8a`, `yacc`, `ar`, `mk` in
`/386/bin`), so `o9build` can compile the generated C and link `libo9.a`
in-guest — only the o9-specific pieces need bundling. The toolchain targets
**both 9front and 9legacy from one compiler**: the facade codegen in
`o9c/grammar.d/50-app-facade.y` wraps its `srvrelease`/`srvacquire` compat shim
in `#ifndef O9_HAVE_SRVRELEASE`, and `o9build` probes the local `<9p.h>` at
facade-build time — defining `O9_HAVE_SRVRELEASE` when the real lib9p API is
visible (9front: requests interleave via the real `srvrelease`/`srvacquire`)
and leaving it undefined otherwise (9legacy: the no-op shim is compiled in,
since 9legacy's lib9p does not expose those symbols). The same `o9c`+`o9build`
therefore works on both; nothing about the binary is 9legacy-specific. (The
prebuilt in `release/386` must be rebuilt in-guest — `mk o9c` then copy `o9c`
into `release/386/bin/` — to emit the guarded shim; an older prebuilt predating
this change emits the shim unconditionally and soft-regresses 9front.)

`-o9` is on by default (default path `../objective-9c`); the build skips it with
a warning if that directory is absent, and errors if the directory exists but
`release/386/` is missing (the prebuilt is required when the language repo is
present). Pass `-o9 ''` to skip explicitly. Idempotent: re-runs overwrite in
place. The bundled source tree at `/usr/glenda/src/objective-9c/` and the
`/usr/glenda/o9legacy-smoke.rc` rebuild+verify harness remain in the base image
for anyone who wants to rebuild the toolchain in-guest.
