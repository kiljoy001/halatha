#!/usr/bin/env python3
"""Check that the new IL sender exchanges datagrams with an old 9front kernel."""

import importlib.util
import sys
from pathlib import Path

spec = importlib.util.spec_from_file_location(
    "ygg_test", Path(__file__).with_name("test-il-ygg-qemu.py")
)
suite = importlib.util.module_from_spec(spec)
spec.loader.exec_module(suite)


def main():
    if len(sys.argv) != 2:
        sys.exit("usage: test-il-compat-qemu.py pair-directory")
    pair = Path(sys.argv[1]).resolve()
    old = suite.VM("a", pair, "mcast=230.0.0.2:23457")
    new = suite.VM("b", pair, "mcast=230.0.0.2:23457")
    try:
        old.boot()
        new.boot()
        for vm, addr in ((old, "10.45.0.1"), (new, "10.45.0.2")):
            vm.run("9fs 9fat", 30)
            vm.run("ip/ipconfig loopback /dev/null 127.1 /128", 30)
            vm.run(f"ip/ipconfig ether /net/ether0 {addr} 255.255.255.0", 30)
            vm.run("cp /n/9fat/halatha/iltest.c /tmp/iltest.c", 30)
            vm.run("8c -o /tmp/iltest.8 /tmp/iltest.c", 60)
            vm.run("8l -o /tmp/iltest /tmp/iltest.8", 60)
        ping = new.run("ip/ping -n 5 10.45.0.1", 30)
        if "ttl" not in ping:
            raise RuntimeError(f"VM underlay unavailable: {ping}")
        old.send("/tmp/iltest server wait")
        new.run("/tmp/iltest 10.45.0.1 wait", 120, "IL PASS 10.45.0.1")
        old.wait(120, "IL SERVER PASS")
        print("NEW -> OLD IL PASS", flush=True)
        new.send("/tmp/iltest server wait")
        old.run("/tmp/iltest 10.45.0.2 wait", 120, "IL PASS 10.45.0.2")
        new.wait(120, "IL SERVER PASS")
        print("OLD -> NEW IL PASS", flush=True)
    finally:
        old.stop()
        new.stop()


if __name__ == "__main__":
    main()
