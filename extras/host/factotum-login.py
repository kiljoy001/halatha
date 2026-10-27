#!/usr/bin/env python3
"""Ask for a password and add it to the local factotum."""

import os
import pwd
import re
import subprocess
import sys
import time
from pathlib import Path


CTL = Path(os.environ.get("HALATHA_FACTOTUM_CTL", Path.home() / "mnt/factotum/ctl"))
DOMAIN_FILE = Path.home() / ".config/halatha/factotum-domain"
DOMAIN_PATTERN = re.compile(r"[A-Za-z0-9][A-Za-z0-9._-]{0,253}\Z")


def dialog(kind, message):
    return subprocess.run(
        ["zenity", f"--{kind}", "--title=Halatha factotum", f"--text={message}"],
        stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, check=False,
    )


def valid_identity(user, domain):
    if not DOMAIN_PATTERN.fullmatch(user) or not DOMAIN_PATTERN.fullmatch(domain):
        raise RuntimeError("user and auth domain must contain only letters, numbers, dots, underscores, or hyphens")


def configured_domain():
    domain = os.environ.get("HALATHA_FACTOTUM_DOMAIN", "").strip()
    if not domain and DOMAIN_FILE.exists():
        domain = DOMAIN_FILE.read_text().strip()
    if not domain:
        answer = dialog("entry", "Enter your factotum auth domain")
        if answer.returncode:
            return None
        domain = answer.stdout.decode("utf-8").strip()
        if not DOMAIN_PATTERN.fullmatch(domain):
            raise RuntimeError("invalid factotum auth domain")
        DOMAIN_FILE.parent.mkdir(parents=True, exist_ok=True)
        DOMAIN_FILE.write_text(domain + "\n")
    if not DOMAIN_PATTERN.fullmatch(domain):
        raise RuntimeError("invalid factotum auth domain")
    return domain


def key_command(password, user, domain):
    if not password or any(char in password for char in "\0\r\n"):
        raise RuntimeError("enter a nonempty, single-line password")
    valid_identity(user, domain)
    # Factotum's attr parser uses Plan 9 single quotes and doubled quotes.
    quoted = password.replace("'", "''")
    return f"key proto=dp9ik dom={domain} user={user} !password='{quoted}'\n".encode()


def main():
    for _ in range(30):
        if CTL.exists():
            break
        time.sleep(1)
    else:
        raise RuntimeError("factotum is not mounted at ~/mnt/factotum")

    user = (os.environ.get("USER") or pwd.getpwuid(os.getuid()).pw_name).strip()
    domain = configured_domain()
    if domain is None:
        return 0
    valid_identity(user, domain)
    prompt = dialog("password", f"Add the password for {user}@{domain} to local factotum")
    if prompt.returncode:
        return 0  # Login continues when the dialog is cancelled.
    password = prompt.stdout.rstrip(b"\r\n").decode("utf-8")
    command = key_command(password, user, domain)
    # Send the secret directly to factotum, without command arguments,
    # a shell, a temporary file, or a log.
    fd = os.open(CTL, os.O_WRONLY | os.O_CLOEXEC)
    try:
        if os.write(fd, command) != len(command):
            raise RuntimeError("factotum accepted only part of the key command")
    finally:
        os.close(fd)
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except (OSError, RuntimeError, UnicodeError) as exc:
        dialog("error", str(exc))
        sys.exit(1)
