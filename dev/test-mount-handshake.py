#!/usr/bin/env python3
"""Run real factotum mount tests in an isolated namespace with ephemeral keys."""
import argparse
import os
from pathlib import Path
import signal
import subprocess
import tempfile
import time
import uuid


def main():
    root = Path(__file__).resolve().parent.parent
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--factotum', default=str(root.parent / 'factotum-dp9ik/factotum'))
    parser.add_argument('--browser', action='store_true')
    parser.add_argument('--target', help='optional real host:port, otherwise a local 9P fixture')
    parser.add_argument('--read', help='public relative file path on the real target')
    parser.add_argument('--expect', default='')
    parser.add_argument('--sha256', default='')
    args = parser.parse_args()
    if args.target and (not args.browser or not args.read):
        parser.error('--target requires --browser and --read')
    factotum = Path(args.factotum).resolve()
    if not factotum.is_file():
        parser.error('build an Ed25519-enabled factotum first')
    service = 'halatha-mount-test-' + uuid.uuid4().hex
    with tempfile.TemporaryDirectory(prefix='halatha-mount-auth-') as tmp:
        namespace = Path(tmp) / 'ns'
        namespace.mkdir(mode=0o700)
        socket = namespace / service
        env = dict(os.environ, NAMESPACE=str(namespace), FACTOTUM_TPM_BACKEND='mock',
                   HALATHA_TEST_FACTOTUM_SOCKET=str(socket),
                   HALATHA_TEST_MOUNT_BROWSER='1' if args.browser else '0')
        # Do not accidentally inherit another run's remote target.
        for key in ['HALATHA_TEST_MOUNT_TARGET', 'HALATHA_TEST_MOUNT_READ',
                    'HALATHA_TEST_MOUNT_EXPECT', 'HALATHA_TEST_MOUNT_SHA256']:
            env.pop(key, None)
        if args.target:
            env.update(HALATHA_TEST_MOUNT_TARGET=args.target, HALATHA_TEST_MOUNT_READ=args.read,
                       HALATHA_TEST_MOUNT_EXPECT=args.expect, HALATHA_TEST_MOUNT_SHA256=args.sha256)
        with open(Path(tmp) / 'factotum.log', 'wb') as log:
            process = subprocess.Popen([str(factotum), '-n', '-s', service], env=env, stdout=log, stderr=log)
            try:
                deadline = time.monotonic() + 5
                while not socket.exists():
                    if time.monotonic() >= deadline:
                        raise RuntimeError('isolated factotum socket did not become ready')
                    time.sleep(0.05)
                subprocess.run(['go', 'test', '-race', './cmd/wanix', '-run',
                                'TestImport9P', '-count=1', '-v'],
                               cwd=root, env=env, check=True, timeout=180)
            finally:
                # Plan9port can daemonize 9pserve. Match only this run's unique
                # service arguments; never stop a user's normal factotum.
                pids = {process.pid}
                for path in Path('/proc').glob('[0-9]*/cmdline'):
                    try:
                        parts = path.read_bytes().split(b'\0')
                        if not parts:
                            continue
                        executable = Path(os.fsdecode(parts[0])).name
                        if executable == 'factotum' and os.fsencode(service) in parts:
                            pids.add(int(path.parent.name))
                        if executable == '9pserve' and os.fsencode('unix!' + str(socket)) in parts:
                            pids.add(int(path.parent.name))
                    except (OSError, ValueError):
                        pass
                for pid in pids:
                    try:
                        os.kill(pid, signal.SIGTERM)
                    except ProcessLookupError:
                        pass
                try:
                    process.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait()


if __name__ == '__main__':
    main()
