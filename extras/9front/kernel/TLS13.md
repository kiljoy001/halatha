# Native TLS 1.3 client in 9front

The local 9front source at `sys/src/9/port/devtls.c` now supports TLS 1.3
records with AES-128-GCM and AES-256-GCM. It uses the TLS 1.3 outer record
type, legacy record version, inner content type, five-byte AAD, and
sequence-number XOR nonce. It rejects records with an invalid authentication
tag. The existing TLS 1.2 path remains available.

The opt-in `tlsclient -3` path uses `libsec/port/tlshand13.c` for a TLS 1.3
client handshake. It supports X25519, AES-128-GCM, and ECDSA P-256 with
SHA-256. It derives handshake and application traffic secrets, verifies the
server CertificateVerify and Finished messages, and requires a matching
certificate or SPKI thumbprint. Pass both `-n servername` and `-t thumbprints`.
The regular `tlsClient`, `tlsServer`, and `tlsclient` paths still negotiate at
most TLS 1.2.

The new client does not yet support RSA certificates, client certificates,
HelloRetryRequest, session resumption, or KeyUpdate. It relies on an explicit
thumbprint; it does not validate a CA chain or certificate lifetime. Do not
use it for unattended, long-lived connections until KeyUpdate is handled.
The fixed keys in the kernel test program are only for isolated VMs.

After opening `/net/tls/clone`, a handshaker can use these controls:

```
fd <network-fd> 0x304
version 0x304
secret13 aes_128_gcm|aes_256_gcm <isclient:0|1> <base64-key-material>
cipher13 in|out|both
opened
```

The decoded material for `secret13` is exactly `client key || server key ||
client IV || server IV`. Each key is 16 or 32 bytes; each IV is 12 bytes.
`cipher13` activates a pending key in the selected direction and resets that
direction's record sequence number. The handshaker must serialize key changes
with record I/O. It can install handshake keys and later replace them with
application keys. The old `secret` and `changecipher` controls reject TLS 1.3.

Build the kernel in a throwaway QEMU image:

```
NINEFRONT_DEVTLS_SOURCE=/home/scott/Repo/9front/sys/src/9/port/devtls.c \
NINEFRONT_TLS13_SOURCE=/home/scott/Repo/9front/sys/src/libsec/port/tlshand13.c \
  python3 extras/9front/kernel/build-kernel.py BASE.img OUTDIR
```

The build copies the source into the VM before compiling. It does not change
the base image or physical machines. The VM helper links `tlshand13.c`
directly into `tlsclient.386` against that VM's installed `libsec`, using
the VM's `libsec.h`; the source tree also registers the new file in the
`libsec` mkfile for later full builds. The tests copy the base image again:

```
python3 dev/test-tls13-kernel-qemu.py BASE.img OUTDIR/9pchalatha PAIRDIR
python3 dev/test-tls13-kernel-reference-qemu.py BASE.img OUTDIR/9pchalatha REFDIR
python3 dev/test-tls13-native-qemu.py BASE.img OUTDIR NATIVEDIR
```

The first test exchanges 32 KiB through two 9front kernels and rejects a
wrong-key record. The second checks records in both directions against
Python's independent AES-GCM implementation for both key sizes. It also
checks an initial ClientHello record using the permitted legacy `0x0301`
version, plus a compatibility ChangeCipherSpec followed by encrypted
handshake and padded application records.

The native test runs a 9front `tlsclient -3` inside QEMU against an
OpenSSL-backed Python TLS 1.3 server on the QEMU host gateway. It checks an
application exchange with a matching certificate pin and rejection with an
incorrect pin. It does not contact physical 9front machines.
