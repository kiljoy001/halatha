#!/usr/bin/env python3
"""Install the local factotum prompt for the current desktop user."""

import argparse
import re
import shutil
import sys
from pathlib import Path


DOMAIN_PATTERN = re.compile(r"[A-Za-z0-9][A-Za-z0-9._-]{0,253}\Z")


def desktop_quote(value):
    return '"' + str(value).replace("\\", "\\\\").replace('"', '\\"') + '"'


def install(home, domain):
    if domain is not None and not DOMAIN_PATTERN.fullmatch(domain):
        raise ValueError("invalid factotum auth domain")

    source = Path(__file__).with_name("factotum-login.py")
    script = home / ".local/bin/halatha-factotum-login.py"
    launcher = home / ".config/autostart/halatha-factotum.desktop"
    script.parent.mkdir(parents=True, exist_ok=True)
    launcher.parent.mkdir(parents=True, exist_ok=True)
    shutil.copyfile(source, script)
    script.chmod(0o700)

    launcher.write_text(
        "[Desktop Entry]\n"
        "Type=Application\n"
        "Name=Halatha factotum login\n"
        "Comment=Add the login password to the local factotum\n"
        f"Exec={desktop_quote(Path(sys.executable))} {desktop_quote(script)}\n"
        "Terminal=false\n"
        "X-GNOME-Autostart-enabled=true\n"
    )

    if domain is not None:
        domain_file = home / ".config/halatha/factotum-domain"
        domain_file.parent.mkdir(parents=True, exist_ok=True)
        domain_file.write_text(domain + "\n")

    return script, launcher


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--domain", help="factotum auth domain; otherwise prompt on first login")
    args = parser.parse_args()
    script, launcher = install(Path.home(), args.domain)
    print(f"Installed {script}\nInstalled {launcher}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
