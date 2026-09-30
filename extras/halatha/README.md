# Halatha Guest Tools

Halatha treats the browser-side Wanix namespace as a Plan 9 service tree. The
guest mounts that tree over 9P2000, then native Plan 9 programs talk to browser
services with ordinary file operations.

The first guest tool is `halctl`, an Objective-9C command for exercising that
bridge from 9legacy:

```rc
halctl status
halctl ls
halctl cat net/status
halctl clip get
halctl clip set 'hello from rio'
halctl write app/ctl start
halctl fetch https://example.com/
halctl export /tmp/report.txt
halctl import upload.txt /tmp/upload.txt
```

`halctl` uses `$halroot` when set. Otherwise it tries `/n/wanix`, then falls
back to the current development mount at `/n/host`.

Browser examples:

```text
examples/basic-v86-9legacy-direct.html?image=halatha&choice=3
examples/basic-vm-9legacy.html
```

The direct v86 page includes a small browser-side 9P2000 server for serial1
(COM2, `/dev/eia1`), so the image's `serial9p` boot hook can mount `/n/host`
without sharing COM1 with boot console output. The
`wanix-vm` example seeds the VM task namespace with the same service tree and
serves it through Wanix's Go 9P2000 bridge.

Expected service paths:

```text
clip/text       browser clipboard text
downloads/      browser download/export egress
fetch/ctl       write URL or request command
fetch/body      read latest fetched response
net/status      WISP/Ygg/browser network status
uploads/        browser file-picker/import ingress
```

## Administrator key registration

The [keyreg Objective-9C GUI](keyreg/README.md) manages explicit babyfs key
registration and revocation from an authenticated administrator session.
It never automatically enrolls browser or VM identities.
