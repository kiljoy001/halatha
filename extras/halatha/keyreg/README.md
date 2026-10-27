# Babyfs key registration

`keyreg` is an Objective-9C/libdraw administrator application. Run it on
babyfs through an authenticated drawterm/rio session. It is not a public
registration endpoint and does not run with administrator authority inside
an untrusted browser VM.

The application shows enrolled public keys, their allowed transport, and
storage policy. Select a row to see the complete keys, or paste a new client
public key and the permitted forwarding node's public key. Click **Review
registration**, inspect the exact keys, then **Register**. This explicitly
creates that client's private read/write 1 GiB filesystem. Generating a key,
refreshing the list, and requesting a mount never enroll anything.

For revocation, select a client, click **Review revocation**, then **Revoke**.
This removes authorization for future connections. It retains the backing
image and does not terminate already authenticated sessions. Re-enrolling a
retained image requires administrator recovery; the provisioning script
refuses to reformat existing images.

Changing either field invalidates the pending review. Cancel also clears it.
The application accepts canonical 64-character lowercase hex public keys,
never private keys. Commands use fixed executable paths and separate argument
vectors, with no shell interpolation. Permissions come from the administrator
session, not from the GUI.

## Build and run

On a native 9front machine with Objective-9C installed:

```rc
o9build keyreg.o9
o9build keyreg-test.o9
./keyreg-test
```

Install `keyreg` as `/usr/scott/bin/amd64/keyreg` on babyfs. The backend must
include the updated `ygg9p -list` and `ygg9p -revoke` commands and the enrollment
script that accepts `CLIENT_PUBLIC_KEY [TRANSPORT_PUBLIC_KEY]`.

Launch from an administrator's rio terminal:

```rc
/usr/scott/bin/amd64/keyreg
```

Or start it through graphical drawterm:

```sh
drawterm -h babyfileserver.rentonsoftworks.coin -u scott \
  -c /usr/scott/bin/amd64/keyreg
```

The window needs approximately 980 by 760 pixels. Escape closes it. Registry
rows are paginated ten at a time. Provisioning reports progress before running
and currently waits for the administrative command to finish.

The backend serializes read/modify/publish operations with an exclusive
`config.json.lock` and preserves timestamped backups. If an administrator
process crashes while holding a lock, inspect the state before manually
removing the stale lock. Do not delete a lock owned by a running administrator.
