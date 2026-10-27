#!/usr/bin/env python3
"""Boot an isolated 9front image and exercise IL over IPv4 and IPv6 loopback.

The image must contain the test kernel as 9pchal in 9fat and the local IL
test source as halatha/iltest.c. This runner never connects the VM to a
network or writes the base image used by the kernel builder.
"""

import importlib.util
import subprocess
import sys
import time
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent
BUILDER = REPO / "extras/9front/kernel/build-kernel.py"
spec = importlib.util.spec_from_file_location("kernel_builder", BUILDER)
builder = importlib.util.module_from_spec(spec)
spec.loader.exec_module(builder)


def main():
    if len(sys.argv) != 2:
        sys.exit("usage: test-il-qemu.py test.img")
    disk = Path(sys.argv[1]).resolve()
    logpath = disk.with_name("il-test-serial.log")
    qemu = [
        "qemu-system-i386", "-m", "1024", "-display", "none",
        "-monitor", "none", "-serial", "stdio", "-net", "none",
        "-no-reboot", "-drive", f"file={disk},format=raw,if=ide,index=0",
    ]
    with logpath.open("w") as log:
        proc = subprocess.Popen(qemu, stdin=subprocess.PIPE,
                                stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
        con = builder.Console(proc, log)
        try:
            for _ in range(2):
                if con.expect(["init: starting /bin/rc"], 180) is None:
                    raise RuntimeError("VM did not reach rc")
            if con.expect(["term% "], 60) is None:
                raise RuntimeError("VM did not reach console prompt")

            def run(command, timeout=60, want=None):
                time.sleep(0.5)
                con.buf = ""
                log.flush()
                start = logpath.stat().st_size
                con.send(command)
                if con.expect(["term% "], timeout) is None:
                    raise RuntimeError(f"timed out running {command}")
                log.flush()
                output = logpath.read_bytes()[start:].decode(errors="replace")
                if want is not None and want not in output:
                    raise RuntimeError(f"{command} failed: {output[-1200:]}")
                return output

            run("9fs 9fat", 30)
            run("ip/ipconfig loopback /dev/null 127.1 /128", 30)
            run("ip/ipconfig loopback /dev/null ::1 /128", 30)
            run("cp /n/9fat/halatha/iltest.c /tmp/iltest.c", 30)
            run("8c -o /tmp/iltest.8 /tmp/iltest.c", 60)
            run("ls -l /tmp/iltest.8", 30, "/tmp/iltest.8")
            run("8l -o /tmp/iltest /tmp/iltest.8", 60)
            run("ls -l /tmp/iltest", 30, "/tmp/iltest")
            for address in ("127.0.0.1", "::1"):
                marker = f"IL LOOPBACK PASS {address}"
                run(f"/tmp/iltest {address}", 120, marker)
                print(marker, flush=True)
            flow = run("/tmp/iltest 127.0.0.1 slow", 120, "IL LOOPBACK PASS 127.0.0.1")
            if "IL FLOW CREDIT PASS" not in flow:
                raise RuntimeError(f"receive credit did not stop the burst: {flow[-1600:]}")
            print("IL FLOW CREDIT PASS 127.0.0.1", flush=True)
            con.send("fshalt")
            if con.expect(["done halting", "halted"], 60) is None:
                raise RuntimeError("VM did not halt")
        finally:
            proc.kill()
            proc.wait()


if __name__ == "__main__":
    main()
