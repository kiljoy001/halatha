#!/usr/bin/env python3
"""Run an IL transfer between two isolated 9front VMs over Yggdrasil.

The pair directory must contain a.img, b.img, a.conf, b.conf, a.addr,
and b.addr. Both disks must carry 9pchal, halatha/iltest.c, their config,
and halatha/yggdrasil in 9fat. No physical or public network is attached.
"""

import importlib.util
import os
import socket
import subprocess
import sys
import threading
import time
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent
spec = importlib.util.spec_from_file_location(
    "kernel_builder", REPO / "extras/9front/kernel/build-kernel.py"
)
builder = importlib.util.module_from_spec(spec)
spec.loader.exec_module(builder)


class VM:
    def __init__(self, name, pair, netdev):
        self.name = name
        self.logpath = pair / f"{name}-serial.log"
        self.log = self.logpath.open("w")
        disk = pair / f"{name}.img"
        self.monitorpath = pair / f"{name}.mon"
        self.monitorpath.unlink(missing_ok=True)
        monitor = (
            f"unix:{self.monitorpath},server,nowait"
            if os.environ.get("IL_VM_FLAP_SEC") else "none"
        )
        qemu = [
            "qemu-system-i386", "-enable-kvm", "-m", "1024",
            "-display", "none", "-monitor", monitor, "-serial", "stdio",
            "-no-reboot", "-netdev", f"socket,id=n0,{netdev}",
            "-device", f"ne2k_pci,netdev=n0,mac=52:54:00:44:00:{'01' if name == 'a' else '02'}",
            "-drive", f"file={disk},format=raw,if=ide,index=0",
        ]
        delay = os.environ.get("IL_VM_DELAY_US")
        if delay:
            qemu.extend(["-object", f"filter-buffer,id=delay0,netdev=n0,interval={int(delay)},queue=tx"])
        self.proc = subprocess.Popen(qemu, stdin=subprocess.PIPE,
                                     stdout=subprocess.PIPE,
                                     stderr=subprocess.STDOUT)
        self.con = builder.Console(self.proc, self.log)

    def boot(self):
        for _ in range(2):
            if self.con.expect(["init: starting /bin/rc"], 180) is None:
                raise RuntimeError(f"{self.name}: no rc")
        if self.con.expect(["term% "], 60) is None:
            raise RuntimeError(f"{self.name}: no prompt")

    def send(self, command):
        time.sleep(0.5)
        self.con.buf = ""
        self.log.flush()
        self.start = self.logpath.stat().st_size
        self.con.send(command)

    def wait(self, timeout=60, want=None):
        if self.con.expect(["term% "], timeout) is None:
            raise RuntimeError(f"{self.name}: timed out")
        self.log.flush()
        output = self.logpath.read_bytes()[self.start:].decode(errors="replace")
        if want is not None and want not in output:
            raise RuntimeError(f"{self.name}: {want!r} absent: {output[-1600:]}")
        return output

    def run(self, command, timeout=60, want=None):
        self.send(command)
        return self.wait(timeout, want)

    def stop(self):
        self.proc.kill()
        self.proc.wait()
        self.log.close()


def main():
    if len(sys.argv) != 2:
        sys.exit("usage: test-il-ygg-qemu.py pair-directory")
    pair = Path(sys.argv[1]).resolve()
    a = VM("a", pair, "mcast=230.0.0.1:23456")
    b = VM("b", pair, "mcast=230.0.0.1:23456")
    try:
        a.boot()
        b.boot()
        for vm, addr in ((a, "10.44.0.1"), (b, "10.44.0.2")):
            vm.run("9fs 9fat", 30)
            vm.run("ip/ipconfig loopback /dev/null 127.1 /128", 30)
            vm.run("ip/ipconfig loopback /dev/null ::1 /128", 30)
            vm.run(f"ip/ipconfig ether /net/ether0 {addr} 255.255.255.0", 30)
            vm.run("cp /n/9fat/halatha/iltest.c /tmp/iltest.c", 30)
            vm.run("8c -o /tmp/iltest.8 /tmp/iltest.c", 60)
            vm.run("8l -o /tmp/iltest /tmp/iltest.8", 60)
            vm.run(f"cp /n/9fat/halatha/{vm.name}.conf /tmp/ygg.conf", 30)
            vm.run("cp /n/9fat/halatha/yggdrasil /tmp/yggdrasil", 60)
            vm.run("chmod +x /tmp/yggdrasil", 30)
            vm.run("rm -f /tmp/ygg.log", 30)
            vm.run("/tmp/yggdrasil -useconffile /tmp/ygg.conf -logto /tmp/ygg.log &", 30)
        underlay = b.run("ip/ping -n 5 10.44.0.1", 30)
        if "ttl" not in underlay:
            raise RuntimeError(f"VM underlay unavailable: {underlay}")
        print("VM underlay PASS", flush=True)
        for _ in range(12):
            ygglog = b.run("tail -12 /tmp/ygg.log", 30)
            if "Connected outbound" in ygglog:
                break
            time.sleep(5)
        else:
            raise RuntimeError(f"Yggdrasil peer did not connect: {ygglog}")
        a.send("/tmp/iltest server")
        peer = (pair / "a.addr").read_text().strip()
        overlay = b.run(f"ip/ping -6 -n 2 {peer}", 20)
        if "ttl" not in overlay:
            raise RuntimeError(f"Yggdrasil overlay unavailable: {overlay}")
        print("Yggdrasil overlay PASS", flush=True)
        flap_seconds = float(os.environ.get("IL_VM_FLAP_SEC", "0"))
        flap_errors = []
        if flap_seconds:
            def flap_link():
                try:
                    time.sleep(3)
                    with socket.socket(socket.AF_UNIX) as mon:
                        mon.connect(str(b.monitorpath))
                        mon.settimeout(5)
                        def response():
                            data = b""
                            while b"(qemu)" not in data:
                                data += mon.recv(4096)
                            return data.decode(errors="replace")
                        response()
                        mon.sendall(b"set_link n0 off\n")
                        off = response()
                        if "not found" in off.lower() or "error" in off.lower():
                            raise RuntimeError(off)
                        time.sleep(flap_seconds)
                        mon.sendall(b"set_link n0 on\n")
                        on = response()
                        if "not found" in on.lower() or "error" in on.lower():
                            raise RuntimeError(on)
                    print(f"VM link restored after {flap_seconds:g}s", flush=True)
                except Exception as error:
                    flap_errors.append(error)
            flap_thread = threading.Thread(target=flap_link, daemon=True)
            flap_thread.start()
        b.run(f"/tmp/iltest {peer}", 120, f"IL PASS {peer}")
        if flap_seconds:
            flap_thread.join(10)
            if flap_errors:
                raise RuntimeError(f"VM link flap failed: {flap_errors[0]}")
        a.wait(120, "IL SERVER PASS")
        print(f"IL YGG PASS {peer}", flush=True)

        a.run("echo IL-9P-OK >/tmp/il-9p-data", 30)
        a.send("aux/listen1 -t il!*!17008 /bin/exportfs -m 1400 -r /tmp")
        b.run("mkdir -p /n/il-ygg", 30)
        b.run(f"srv -m il!{peer}!17008 il-ygg /n/il-ygg", 90)
        b.run("cat /n/il-ygg/il-9p-data", 30, "IL-9P-OK")
        print("IL 9P MOUNT PASS", flush=True)
    finally:
        a.stop()
        b.stop()


if __name__ == "__main__":
    main()
