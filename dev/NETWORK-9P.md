The browser imports a fixed TCP 9P2000 export only after an Ed25519
challenge-response handshake. Factotum holds the signing key on the local
host. The browser receives a signature, never the private key.

```text
Browser: click Mount with factotum
       |
       | WebSocket: receive 30-second challenge
       | POST session ID + local pairing credential
       v
Local gateway signing helper --> local factotum Unix socket
       |                         proto=ed25519, service=halatha-mount
       | <-- public key + signature
       v
Browser: send proof on the original WebSocket
       |
Gateway: approved key? valid signature? fresh connection-bound challenge?
       |
       | only after success: open the configured TCP endpoint
       v
9P file server --> browser namespace: network/
```

The signed message is the exact UTF-8 JSON payload issued by the gateway,
transported as unpadded base64url. It includes protocol `halatha-mount-v1`,
a random 256-bit session ID and nonce, gateway origin, target, export name,
read-only access, upstream user/aname, browser origin, and expiry. Clients
sign the decoded bytes without reserializing JSON. The proof contains
`type=mount-proof`, session ID, hex public key and hex signature. The gateway
consumes the challenge once on the issuing connection and replies
`type=mount-ready` before accepting binary 9P. Disconnecting requires a new
handshake. No reusable mount bearer token is issued.

Set up the local identity:

1. Build the Ed25519-enabled `../factotum-dp9ik` implementation.
2. Through its private `ctl`, load a key with `proto=ed25519` and
   `service=halatha-mount`. `!seed` accepts a 32-byte seed; `!sk` accepts
   Go/Yggdrasil's 64-byte seed-plus-public-key format. Provision this through
   trusted local tooling; do not paste the secret into the browser or put it
   in command arguments. Use `confirm` with a working confirmation agent if
   each key use should require approval.
3. Put the approved 32-byte public key in a local text file as 64 hex
   characters, one key per line. All approved keys authorize this one fixed,
   read-only export. The public key must match the selected factotum key.
4. Identify the local factotum Unix socket (for plan9port, normally under its
   namespace directory). The helper uses the local `USER` for Tattach; use
   `--import-9p-factotum-user` to override it.

Build and run:

```sh
go run buildjs.go
GOOS=js GOARCH=wasm go build -o dist/wanix.debug.wasm ./wasm
go build -o /tmp/halatha-network-wanix ./cmd/wanix
/tmp/halatha-network-wanix serve --listen 127.0.0.1:7654 --wisp=false \
  --telemetry-dir '' \
  --import-9p-target '[SERVER_YGGDRASIL_ADDRESS]:564' \
  --import-9p-server-key SERVER_PUBLIC_KEY_HEX \
  --import-9p-root '/usr/ADMIN/lib/ygg9p/mnt/CLIENT_PUBLIC_KEY_HEX/data' \
  --import-9p-keys /path/outside/webroot/approved-public-keys \
  --import-9p-factotum /path/to/private/namespace/factotum \
  --import-9p-pairing-file /tmp/halatha-mount-pairing .
```

Import mode requires an explicit loopback listen address and an approved-key
file; there is no anonymous fallback. The gateway writes a random pairing
credential to the specified file with mode 0600. That file must be outside
the HTTP-served tree. Each restart replaces the credential. Read it locally,
open http://127.0.0.1:7654/examples/network-9p.html, enter the credential, and
click **Mount with factotum**. The page clears the input after submission.

Host and Origin must match the configured origin exactly, including scheme
and port. `--import-9p-origin` overrides the default derived from the listen
address, for example when using `localhost` rather than `127.0.0.1`. The
browser signing helper currently supports same-origin use. Requests with no
Origin are rejected. Native clients can supply an Origin, but still require
a valid signature; the local signing helper additionally requires pairing.
It accepts only a pending session ID, not arbitrary signing bytes, key
selectors, or target addresses.

For Yggdrasil transport, configure the target as the server's Yggdrasil
IPv6 address, for example `[your-yggdrasil-address]:564`, and enforce the
server's Yggdrasil interface/source firewall policy. The handshake itself
does not install Yggdrasil, validate its routes, or force TCP over it. The file
server also requires server-side signed Tauth; enroll the connecting node's
public key there first (see the file server's own deployment notes).

The gateway parses complete 9P requests across WebSocket boundaries. It
allows only 9P2000, the fixed `none` user and configured aname, read-only
opens, walks, reads, stats, clunks, and flushes. Write/create/remove/wstat,
truncate/remove-on-close opens, alternative identities/exports, and other
dialects close the connection before that request reaches the upstream.
Use `--import-9p-aname` for a fixed nonempty upstream attach name. `network`
is the browser export label; actual upstream scope comes from that dedicated
server export and aname, not a browser-selected path.

The key file is loaded at startup. To revoke a key, edit it and restart the
gateway, which also disconnects existing mounts and rotates pairing. There
are at most 128 active/authorizing connections and proofs expire after 30
seconds. Pairing grants the trusted page use of the selected local key;
compromised same-origin code can exercise that granted access.

This protects the gateway route. It does not add authentication to the
upstream 9front server or block another client dialing an independently
reachable port 564. Restrict that service separately. This browser test
mounts the Wanix namespace, not the guest VM namespace.

Run real integration tests, with Playwright and Chromium installed:

```sh
# Isolated factotum + ephemeral keys + local 9P fixture + real Chromium.
python3 dev/test-mount-handshake.py --browser

# Same handshake against the actual LAN server, reading a public manual page.
python3 dev/test-mount-handshake.py --browser \
  --target FILESERVER:564 \
  --read sys/man/5/read --expect NAME \
  --sha256 3b669c68801101b29c4e9f5e88f5305d08c5fcf604a5fd5fcf930ceeef7547e3

# Test an already running gateway with its private pairing file.
node dev/test-network-9p-e2e.mjs \
  http://127.0.0.1:7654/examples/network-9p.html \
  --pairing-file=/tmp/halatha-mount-pairing \
  --read=sys/man/5/read --expect=NAME
```

The isolated runner starts a separate factotum namespace, inserts only
random ephemeral keys, and removes its processes afterward. It never
loads the user's Yggdrasil private key. `--factotum` selects another binary.
The tests cover replay, expiry, unknown keys, changed scope, invalid
Origin/Host, unpaired signing, arbitrary signing requests, pre-auth 9P,
and raw write requests. Failure tests assert that the upstream is not dialed
before authorization. `--expect-failure` on the standalone browser test can
assert an unavailable upstream after authentication. No file contents or
private signing/pairing keys are printed.

Declare an authenticated import with:

```html
<wanix-bind type="import" dst="network" src="/.well-known/import9p"
            transport="websocket" protocol="9P2000" user="none"
            auth="factotum"></wanix-bind>
```

Set the element's `mountPairingToken` property before attaching it to the
DOM. Existing imports without `auth` keep their existing transport behavior.
The browser 9P client remains read-only, uses one RPC at a time, and does
not implement Plan 9 Tauth exchanges or walks beyond 16 components.
