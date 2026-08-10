# Halatha

Halatha is a browser-native compute runtime that brings a Unix — and now
Plan 9 — environment to the web. It is a significantly divergent fork of
[Wanix](https://github.com/tractordev/wanix): it keeps Wanix's embeddable
namespace runtime (the `<wanix-namespace>` HTML elements, Wasm/JavaScript
tasks, and v86 virtualization) and builds on it a deep, native
Plan 9 / 9legacy guest integration that stock Wanix does not have.

## How Halatha differs from stock Wanix

Stock Wanix boots Linux in v86 and bridges the guest to the host over a
per-byte serial 9P channel. Halatha replaces and extends that:

- **Plan 9 / 9legacy as first-class guests.** Boot 9front or 9legacy
  (Bell Labs 4th-edition 386) from whole-disk images into a VGA/rio display
  — not just Linux. The guest kernel lives on the image, so no host
  `bzImage` or host9p root is used. (`examples/basic-vm-9front.html`,
  `examples/basic-vm-9legacy.html`)
- **virtio-9p host bridge.** A guest-kernel `devvirtio9p` driver (`#9`)
  gives the 9legacy guest a message-granular, plain-9P2000 mount of the
  host namespace at `/n/host` — replacing Wanix's slow per-byte serial 9P
  bridge. Host files appear as a native 9P tree inside the guest.
- **Go 9P file server (`fs/p9p`).** A 9P file server in Go that serves the
  wanix kernel namespace to the guest across the virtio-9p transport.
- **WebUSB through the guest USB stack.** A software-only USB
  host-controller-interface driver (`usbns`) in the 9legacy kernel
  registers a root hub with `devusb`, so the stock `usbd` and the
  unmodified `usb/disk`, `usb/serial`, and `usb/ether` class drivers come
  up at boot and forward transfers to the browser's WebUSB bridge. Stock
  Wanix has no USB story at all.
- **9legacy image builder (`extras/9front/mk9legacyimg`).** A Go tool that
  assembles the guest images: patches the gzroot, installs the virtio-9p
  entries and the USB userland, layers guest tools and the troff/hyphen
  fix, grows the gzroot, and writes PLAN9.INI — producing the 16 MB base
  and 40 MB rich (troff / ygg / o9 / halctl / C toolchain) production
  images.
- **halatha guest extras (`extras/halatha`).** `halctl` and supporting
  guest tooling.

The sections below document the Wanix embeddable runtime, carried forward
essentially unchanged as Halatha's base.

## Wanix runtime

Wanix is an embeddable runtime that brings a Unix-like environment to the
browser. Declare a `<wanix-namespace>`, bind files and archives into it,
run Wasm and JavaScript tasks, boot Linux or Plan 9 in an x86 emulator,
and wire up terminals and a VS Code workbench, all from HTML.

```html
<wanix-namespace>
  <wanix-bind type="file" dst="helloworld.wasm" src="./helloworld.wasm"></wanix-bind>
  <wanix-task cmd="helloworld.wasm" term start></wanix-task>
  <wanix-term path="#task/1/term"></wanix-term>
</wanix-namespace>
```

- **Everything is a file.** Processes, terminals, VMs, browser APIs, and storage are exposed through a unified namespace you compose with binds. The same idea as Plan 9, with improvements, in the browser.
- **Composable environments.** Layer tar archives, fetch remote files, write inline scripts, and union directories to build exactly the filesystem your app needs.
- **Pluggable compute.** Run Go/TinyGo (`gojs`), WASI Wasm, JavaScript workers, and x86 Linux or Plan 9 (via v86) as tasks in the same namespace.
- **Isolation by design.** Each task gets its own namespace. VMs export a guest namespace. Import remote namespaces over 9P.
- **Browser-native integration.** OPFS persistence, DOM control, web workers, service workers, and fetch are first-class via the `#web` namespace.
- **No backend required.** The runtime (`wanix.min.js` + `wanix.wasm`) runs entirely client-side. Host static assets on any CDN.
- **Progressive complexity.** Start with a single Wasm binary and terminal. Add Linux or Plan 9 VMs, a full IDE workbench, or cross-origin federation when you need them.

## Plan 9 / 9legacy guests

### Boot 9front in v86

Boot the 386 build of 9front to a VGA/rio display instead of Linux. 9front
boots from a whole-disk image (its own kernel lives on the image), so no
`bzImage` or host9p root is used — `acpi` is required (9front needs it under
v86) and the `screen` attribute gives the VM a VGA canvas:

```html
<wanix-namespace>
  <wanix-bind dst="#vm/v86" type="archive"
    src="https://cdn.jsdelivr.net/npm/wanix-extras@0.4.0-rc2/dist/v86.tgz">
  </wanix-bind>
  <!-- hda: a CORS-enabled, Range-capable URL for the 9front 386 disk image -->
  <wanix-vm hda="https://example.com/9front-386.iso"
    acpi boot="c" mem="128M" screen start
    style="width: 100%; height: 100%;"></wanix-vm>
</wanix-namespace>
```

See [examples/basic-vm-9front.html](examples/basic-vm-9front.html).

### Boot 9legacy with the virtio-9p host bridge

9legacy boots the same way, and with the virtio-9p kernel built in it
mounts the host namespace at `/n/host` over a kernel `#9` device — so the
guest can read and write host files as a native 9P tree:

```html
<wanix-namespace>
  <wanix-bind dst="#vm/v86" type="archive"
    src="https://cdn.jsdelivr.net/npm/wanix-extras@0.4.0-rc2/dist/v86.tgz">
  </wanix-bind>
  <wanix-vm hda="https://example.com/9legacy-wanix-virtio9p-full-usb.img"
    acpi boot="c" mem="128M" screen start
    style="width: 100%; height: 100%;"></wanix-vm>
</wanix-namespace>
```

The rich image (`9legacy-wanix-virtio9p-full-usb.img`) carries the
virtio-9p driver, the USB userland, and the `usbns` HCI in its kernel, and
boots `usbd` automatically. See
[examples/basic-vm-9legacy.html](examples/basic-vm-9legacy.html) and the
direct page
[examples/basic-v86-9legacy-direct.html](examples/basic-v86-9legacy-direct.html).

### WebUSB devices through `usbns`

With the `usbns`-enabled image, the guest's stock USB stack is alive at
boot (`/srv/usb` is posted, a root hub exists under `/dev/usb`, and there
is no `usbd: /dev/usb: no hubs`). Open a USB device from the page (the
`wusb` wrapper or the page picker claims it via the browser WebUSB API),
and the unmodified class drivers attach:

- `usb/disk` mounts a mass-storage stick in the guest.
- `usb/serial` exposes a CDC-ACM device as `/dev/eiaN`.
- `usb/ether` brings up a USB Ethernet adapter.

Transfers flow guest `devusb` → `usbns` HCI → 9P files under
`/n/host/usb/<id>/{req,resp}` → the page's WebUSB bridge. No guest-side
driver code was written for the bridge; the native Plan 9 USB stack drives
real browser-claimed devices.

## Use Cases

- **In-browser dev environments**: edit/run code without a remote container.
- **Interactive demos and tutorials**: embed reproducible sandboxes in docs, blog posts, and courseware.
- **Local-first apps**: persist user data in OPFS, run logic in workers, and build your own platform.
- **Agent sandboxes**: utilize browser sandboxing to isolate an agent environment you construct.
- **Personal compute**: build your own computing environment / operating system — Halatha's Plan 9 guests make a real Plan 9 desktop runnable from a static web page.

## Quick Start (CDN)

```html
<!DOCTYPE html>
<html>
<head>
  <script type="module"
    src="https://cdn.jsdelivr.net/npm/wanix@0.4.0-rc2/dist/wanix.min.js">
  </script>
</head>
<body style="height: 100vh; margin: 0;">

  <wanix-namespace>
    <!-- bind alloc ramfs to namespace root -->
    <wanix-bind dst="." src="#ramfs/new"></wanix-bind>
    <!-- bind inline file into namespace -->
    <wanix-bind type="file" dst="hello.sh" perm="0755">
      echo "Hello from Halatha!"
    </wanix-bind>
    <!-- bind wasm executable from url -->
    <wanix-bind type="file" dst="rc.wasm"
      src="https://cdn.jsdelivr.net/npm/wanix-extras@0.4.0-rc2/dist/rc.wasm">
    </wanix-bind>
    <!-- declare a task that will autostart -->
    <wanix-task id="shell" cmd="rc.wasm -c hello.sh" term start></wanix-task>
    <!-- show a terminal wired up to the task -->
    <wanix-term path="#task/shell/term"></wanix-term>
  </wanix-namespace>

</body>
</html>
```

### JavaScript API

After `<wanix-namespace>` fires a `ready` event, use the filesystem handle:

```html
<script type="module">
  const sys = document.querySelector('wanix-namespace');
  sys.addEventListener('ready', async () => {
    console.log(await sys.root.readDir('.'));
    await sys.root.writeFile('note.txt', 'saved from JS');
  });
</script>
```

Full API: [api/handle.js](api/handle.js).


## Elements Reference

Elements let you compose a Wanix system in HTML. The only visual elements are
`<wanix-term>` and `<wanix-workbench>`.

### `<wanix-namespace>`

The root namespace and Wasm kernel. All other elements live inside it, reference it with `for`, or have an implicit namespace because they are the
root element. Any element other than `<wanix-bind>` can be used as the root element instead of `<wanix-namespace>` and will additionally take its
attributes. 

| Attribute | Description |
|-----------|-------------|
| `wasm` | URL to the Wanix Wasm module. Defaults to `./wanix.wasm` next to the bundle. |
| `debug` | Enable DevTools helpers and verbose logging. |
| `id` | System identifier used for cross-origin import/export. |
| `allow-origins` | Space-separated origins allowed to import this system via `postMessage` (use `*` to allow all). Requires `id`. |

Events: `ready` (namespace usable), `error` (load failure).

### `<wanix-bind>`

Mount a source into the namespace at `dst`. 

| Attribute | Description |
|-----------|-------------|
| `dst` | Destination path. `.` is the namespace root. Paths *do not* start with `/`. |
| `src` | Source path or URL. System paths use `#` prefix (e.g. `#ramfs`, `#web/opfs`). |
| `type` | Bind type (see below). Default: `ns`. |
| `perm` | File permission mode for `file` binds. Default: `0644`. |
| `union` | Union mode when binding to an existing directory. Default: `after`. |

#### Bind types

| Type | Behavior |
|------|----------|
| `ns` | Bind another namespace path (default). |
| `file` | Write element text content (or fetched URL if `src` is set) to `dst`. |
| `archive` | Fetch a `.tar` or `.tar.gz` and mount as a directory tree. |
| `import` | Import a remote Wanix namespace via WebSocket (`ws://` / `wss://`) or iframe + 9P (`src` URL with `#system-id`). |

### `<wanix-task>`

Allocate and run a task, which is shaped like a process (args, env, stdio, ...) and executed by
a task driver. Tasks run in their own namespace, by default inheriting the current/root namespace.

| Attribute | Description |
|-----------|-------------|
| `cmd` | Command line to run. |
| `type` | Task driver: `auto`, `gojs`, `wasi`, `js`, etc. Default: `auto`. |
| `role` | Semantic role. Use `shell` for workbench shell templates. |
| `id` / `alias` | Optional name for referencing the task at `#task/<id>/…`. |
| `env` | Environment variables, space-separated `KEY=VALUE` pairs (use quotes for values with spaces). |
| `wd` | Working directory within the task namespace. |
| `fsys` | Base filesystem path for the task namespace. |
| `stdin` / `stdout` / `stderr` | Namespace paths for I/O redirection. |
| `term` | Allocate a terminal device for this task. |
| `start` | Start the task automatically when the system is ready. |
| `for` | ID of a `<wanix-namespace>` to attach to (instead of being a direct child). |

Terminal path after allocation: `#task/<id>/term` (or `#task/<rid>/term` without alias).

#### Task drivers

| Type | Behavior |
|------|----------|
| `auto` | Determine automatically (default). |
| `js` | Run plain JavaScript as a task. |
| `gojs` | Run Wasm compiled by Go using `GOOS=js GOARCH=wasm`. |
| `wasi` | Run any Wasm compiled using `wasi`/`wasip1`. |


### `<wanix-vm>`

Allocate and run a virtual machine.

| Attribute | Description |
|-----------|-------------|
| `type` | VM backend. Default: `v86`. |
| `id` / `alias` | Optional name. Terminal at `#vm/<id>/term`. |
| `fsys` | Root filesystem path in the namespace. |
| `term` | Allocate a terminal for serial console I/O. |
| `start` | Boot automatically when ready. |
| `append` | Kernel command line additions. |
| `export` | Host-export device (e.g. `ttyS0`, `hvc1`) for guest ↔ host bridging. |
| `mem` | RAM size (e.g. `512M`, `1G`). |
| `boot`, `bios`, `netdev`, … | Additional QEMU-style flags mapped from attribute names. |

Using `<wanix-vm>` requires a VM backend to be loaded using bind to `#vm/<type>`:

```html
<wanix-bind dst="#vm/v86" type="archive" src="https://cdn.jsdelivr.net/npm/wanix-extras@0.4.0-rc2/dist/v86.tgz"></wanix-bind>
```

### `<wanix-term>`

Render an [xterm.js](https://xtermjs.org/) terminal connected to a Wanix terminal device.

| Attribute | Description |
|-----------|-------------|
| `path` | Terminal device path (e.g. `#term/1`, `#task/shell/term`, `#vm/1/term`). |
| `raw` | Raw mode — no local line editing; bytes pass through directly. Use for VM serial consoles. |
| `for` | ID of a `<wanix-namespace>` to attach to. |

Style the element with `height: 100%` (and flex layout on parents) for full-page terminals.

### `<wanix-workbench>`

Embed a VS Code web workbench backed by the Wanix filesystem.

| Attribute | Description |
|-----------|-------------|
| `assets` | URL prefix for workbench static assets (built with `make -C workbench`). |
| `wd` | Workspace folder path in the namespace (e.g. `root`, `.`). |
| `open` | Space-separated file paths to open on startup. |
| `term` | Enable integrated terminal panel. |
| `raw` | Raw terminal mode for integrated terminal. |
| `sidebar` | Initial sidebar state: `default`, `hidden`, `never` (hidden even if user previously opened), or `always` (open even if user previously closed). |
| `panel` | Initial panel state: `default`, `hidden`, `never`, `always`, `max` (seed maximized), or `always-max` (maximized even if user previously restored). |
| `fresh` | Clear stored workbench UI/profile state before startup (do not restore previous layout). |
| `welcome` | Show welcome page on startup. |
| `debug` | Verbose workbench logging. |
| `task-ns`, `term-ns` | Override task/terminal namespace paths (e.g. for VM guest shells). |

Include a child `<wanix-task role="shell" …>` as the shell template.

## System Namespace

These `#` paths are provided by the kernel and can be bound into your namespace:

| Path | Description |
|------|-------------|
| `#task` | Process control and task namespaces. |
| `#term` | Terminal devices. |
| `#vm` | Virtual machine control. |
| `#ramfs` | In-memory filesystem (cloned per bind). |
| `#pipe` | Pipe pairs (cloned per bind). |
| `#signal` | Signal devices (cloned per bind). |
| `#web` | Browser integration — OPFS (`#web/opfs`), DOM, workers, caches, etc. |
| `#wanix` | Internal Wanix devices. |


## Recipes

### Minimal Wasm terminal

Run a Go/TinyGo Wasm binary with a terminal:

```html
<wanix-namespace>
  <wanix-bind type="file" dst="app.wasm" src="https://example.com/app.wasm"></wanix-bind>
  <wanix-task id="app" cmd="app.wasm" term start></wanix-task>
  <wanix-term path="#task/app/term"></wanix-term>
</wanix-namespace>
```

See [examples/basic-terminal.html](examples/basic-terminal.html).

### Writable namespace with inline files

Create a virtual filesystem to use via JS:

```html
<wanix-namespace>
  <wanix-bind dst="." src="#ramfs/new"></wanix-bind>
  <wanix-bind dst="greeting.txt" type="file" perm="0644">
    Hello, world!
  </wanix-bind>
</wanix-namespace>
```

See [examples/basic-namespace.html](examples/basic-namespace.html).

### JavaScript worker task

Run inline JS in a Wanix task:

```html
<wanix-namespace>
  <wanix-bind dst="." src="#ramfs/new"></wanix-bind>
  <wanix-bind dst="task.js" type="file" perm="0766">
    console.log("JS task running!");
  </wanix-bind>
  <wanix-task cmd="task.js" start></wanix-task>
</wanix-namespace>
```

See [examples/task-js.html](examples/task-js.html).

### Layered root filesystem

Stack archives and overlay individual files — later binds win:

```html
<wanix-namespace>
  <wanix-bind type="archive" dst="root"
    src="https://example.com/base-rootfs.tar.gz"></wanix-bind>
  <wanix-bind type="archive" dst="root"
    src="https://example.com/overlay.tar.gz"></wanix-bind>
  <wanix-bind type="file" dst="root/boot/bzImage"
    src="https://example.com/custom-kernel"></wanix-bind>
</wanix-namespace>
```

See [examples/bind-types.html](examples/bind-types.html).

### Boot Linux in v86

```html
<wanix-namespace>
  <wanix-bind dst="." type="archive"
    src="https://cdn.jsdelivr.net/npm/wanix-extras@0.4.0-rc2/dist/wanix-linux.tgz">
  </wanix-bind>
  <wanix-bind dst="#vm/v86" type="archive"
    src="https://cdn.jsdelivr.net/npm/wanix-extras@0.4.0-rc2/dist/v86.tgz">
  </wanix-bind>
  <wanix-vm export="ttyS0" mem="1G" term start></wanix-vm>
  <wanix-term path="#vm/1/term" raw></wanix-term>
</wanix-namespace>
```

See [examples/basic-vm.html](examples/basic-vm.html).

### VS Code workbench with rc shell

Host workbench assets locally (`make -C workbench`), then:

```html
<wanix-namespace debug>
  <wanix-bind type="archive" dst="root"
    src="https://cdn.jsdelivr.net/npm/wanix-extras@0.4.0-rc2/dist/wanix-linux.tgz">
  </wanix-bind>
  <wanix-bind type="fetch" dst="rc.wasm"
    src="https://cdn.jsdelivr.net/npm/wanix-extras@0.4.0-rc2/dist/rc.wasm">
  </wanix-bind>
  <wanix-workbench assets="/workbench" term>
    <wanix-task role="shell" cmd="rc.wasm"></wanix-task>
  </wanix-workbench>
</wanix-namespace>
```

See [examples/basic-workbench.html](examples/basic-workbench.html).

### Workbench with VM guest shell

Edit files on the host namespace while running shells inside a Linux VM:

```html
<wanix-namespace debug>
  <wanix-bind dst="." type="archive" src="/assets/wanix-linux.tgz"></wanix-bind>
  <wanix-bind dst="#vm/v86" type="archive" src="/assets/v86.tgz"></wanix-bind>
  <wanix-vm export="ttyS0" start></wanix-vm>
  <wanix-workbench assets="/workbench"
    task-ns="#vm/1/guest/#task"
    term-ns="#vm/1/guest/#term"
    raw term>
    <wanix-task role="shell" cmd="bin/sh"></wanix-task>
  </wanix-workbench>
</wanix-namespace>
```

See [examples/vm-workbench.html](examples/vm-workbench.html).

### OPFS-backed persistent editor

Persist files in the browser with Origin Private File System:

```html
<wanix-namespace>
  <wanix-bind dst="." src="#web/opfs"></wanix-bind>
  <wanix-bind dst="main.js" type="file" perm="0644">
    export default function() { return 42; }
  </wanix-bind>
  <wanix-workbench open="main.js" assets="/workbench"></wanix-workbench>
</wanix-namespace>
```

### Export and import namespaces

Export a namespace from one page:

```html
<wanix-namespace id="main" allow-origins="*">
  <wanix-bind dst="." src="#ramfs/new"></wanix-bind>
  <wanix-bind dst="shared.txt" type="file">shared data</wanix-bind>
</wanix-namespace>
```

Import it from another page:

```html
<wanix-namespace>
  <wanix-bind type="import" dst="remote"
    src="https://other.example/app.html#main"></wanix-bind>
  <wanix-task id="repl" cmd="rc.wasm" term start></wanix-task>
  <wanix-term path="#task/repl/term"></wanix-term>
</wanix-namespace>
```

Import over WebSocket 9P:

```html
<wanix-bind type="import" dst="home" src="wss://example.com/9p"></wanix-bind>
```

See [examples/example-export.html](examples/example-export.html) and [examples/bind-import.html](examples/bind-import.html).

### Remote VM in a local workbench

Import a VM running on another origin and attach a workbench to its guest namespace:

```html
<wanix-namespace debug>
  <wanix-bind type="import" dst="remote"
    src="https://vm-host.example/linux.html#linux"></wanix-bind>
  <wanix-workbench assets="/workbench"
    task-ns="remote/vm/1/guest/#task"
    term-ns="remote/vm/1/guest/#term"
    raw term>
    <wanix-task role="shell" cmd="bin/sh"></wanix-task>
  </wanix-workbench>
</wanix-namespace>
```

See [examples/import-workbench.html](examples/import-workbench.html).

### More examples

See all [examples](examples).


## Building the 9legacy guest images

The Plan 9 guest images are produced by the Go image builder in
`extras/9front`:

```sh
# rich image: virtio-9p + serial console + USB userland + usbd at boot
go run ./extras/9front/mk9legacyimg.go \
  -base local/9legacy-wanix-virtio9p-full.img \
  -out local/9legacy-wanix-virtio9p-full-usb.img \
  -virtio9p -keep-serial-console -usb-boot \
  -addtree /tmp/usb-bin:/386/bin/usb
```

`-usb-boot` sets `wanixusb=1` in PLAN9.INI so the guest's termrc runs
`usbstart` (and thus `usbd`) at boot. See
[extras/9front/README.md](extras/9front/README.md) for the full flag
reference and the kernel build flow.


## Local Development

```sh
make build          # build runtime + wanix CLI
make examples       # serve examples at http://localhost:7070/examples
make                # show all make tasks
```

Using `make examples` will build extras the first time, and will need
Docker running to succeed. Podman users will need to set `DOCKER_CMD=podman`.

See [CONTRIBUTING.md](CONTRIBUTING.md) for the full build guide.

## Contributing

Halatha is developed at [kiljoy001/halatha](https://github.com/kiljoy001/halatha).
It tracks upstream [tractordev/wanix](https://github.com/tractordev/wanix) and
contributes the Plan 9 / 9legacy integration layer. Issues and pull requests
are welcome on the Halatha repo.

## AI Disclosure

Parts of Halatha — particularly the Plan 9 integration layer (the `usbns`
kernel HCI driver, the `devvirtio9p` bridge, the image builder, and the
9legacy userland build flow) — were developed with AI assistance. A human
designs, verifies, and is responsible for every change.

## License

MIT