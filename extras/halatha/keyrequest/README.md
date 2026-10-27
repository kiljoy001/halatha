# VM access requests

`keyrequest` is an Objective-9C GUI that submits registration or transport-change proposals for administrator review. It never enrolls a key or grants filesystem access.

The VM must run its own Yggdrasil node with a working overlay route. The client reads its dial address from `/n/host/boot/babyfs-endpoint`, which the deployment supplies through `/config/babyfs-endpoint`. There is no LAN fallback or IPv4 relay alias. Enter the client signing public key and the VM Yggdrasil node public key; private keys stay in factotum.

On the browser VM, run `keyrequest` from a rio shell. The boot hook stages
the source and a guest-built 386 executable; status appears at
`/n/host/boot/keyrequest-status`. If the executable is unavailable, the
launcher rebuilds it with the bundled Objective-9C toolchain. The source
stays available under `/n/host/boot/keyrequest*.o9` for inspection.
`request-probe.o9` provides a command-line integration probe using the same
networking client.

On babyfs, `ygg9p-requests-start.rc` binds only the Yggdrasil address. Any Yggdrasil node can submit an **unverified** proposal by default. The endpoint exposes only request submission and that connection's receipt, limits the pending queue to 64 entries, accepts at most one pending request per client key, and bounds each connection to 30 seconds. An optional `-transport-key` restricts the submitting node.

The administrator uses `keyreg` to review, verify the requester independently, and explicitly approve or reject. Approval creates a dedicated 1 GiB filesystem for a new key. Actual storage mounts still require the approved transport and signed nonce authentication. A claimed public key in a proposal is not proof of possession.

The VM's Yggdrasil address and public key are published in
`/n/host/boot/ygg-health`; the same report reads the proposal service's 9P
status through the overlay. The administrator sees the connection's observed
Yggdrasil peer beside the proposed key. Proposal delivery has been verified
from the VPS and read-only 9P status from the VM; mounting a private share
still depends on Ed25519 support in the native 9legacy factotum.
