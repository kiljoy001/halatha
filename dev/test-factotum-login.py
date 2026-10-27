#!/usr/bin/env python3
"""Check the local login prompt sends one quoted key to factotum."""

import importlib.util
import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch


SCRIPT = Path(__file__).resolve().parents[1] / "extras/host/factotum-login.py"
INSTALLER = SCRIPT.with_name("install-factotum-login.py")
spec = importlib.util.spec_from_file_location("factotum_login", SCRIPT)
login = importlib.util.module_from_spec(spec)
spec.loader.exec_module(login)


class FactotumLoginTest(unittest.TestCase):
    def test_password_is_quoted_and_written_only_to_factotum(self):
        with tempfile.TemporaryDirectory() as directory:
            ctl = Path(directory) / "ctl"
            ctl.touch()
            prompt = subprocess.CompletedProcess(["zenity"], 0, b"a b'c\n")
            with patch.dict(login.os.environ, {"USER": "alice", "HALATHA_FACTOTUM_DOMAIN": "example.org"}), \
                 patch.object(login, "CTL", ctl), \
                 patch.object(login, "dialog", return_value=prompt) as dialog, \
                 patch.object(login.subprocess, "run", side_effect=AssertionError("extra process")):
                self.assertEqual(login.main(), 0)
            self.assertEqual(ctl.read_bytes(),
                b"key proto=dp9ik dom=example.org user=alice !password='a b''c'\n")
            self.assertNotIn("a b'c", str(dialog.call_args))

    def test_cancel_does_not_add_key(self):
        with tempfile.TemporaryDirectory() as directory:
            ctl = Path(directory) / "ctl"
            ctl.touch()
            prompt = subprocess.CompletedProcess(["zenity"], 1, b"")
            with patch.dict(login.os.environ, {"USER": "bob", "HALATHA_FACTOTUM_DOMAIN": "example.net"}), \
                 patch.object(login, "CTL", ctl), patch.object(login, "dialog", return_value=prompt):
                self.assertEqual(login.main(), 0)
            self.assertEqual(ctl.read_bytes(), b"")

    def test_first_run_saves_domain_for_next_login(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            ctl = root / "ctl"
            ctl.touch()
            domain_file = root / "config/factotum-domain"
            answers = [
                subprocess.CompletedProcess(["zenity"], 0, b"other.example\n"),
                subprocess.CompletedProcess(["zenity"], 0, b"password\n"),
            ]
            with patch.dict(login.os.environ, {"USER": "carol", "HALATHA_FACTOTUM_DOMAIN": ""}), \
                 patch.object(login, "CTL", ctl), \
                 patch.object(login, "DOMAIN_FILE", domain_file), \
                 patch.object(login, "dialog", side_effect=answers) as dialog:
                self.assertEqual(login.main(), 0)
            self.assertEqual(domain_file.read_text(), "other.example\n")
            self.assertEqual(ctl.read_bytes(),
                b"key proto=dp9ik dom=other.example user=carol !password='password'\n")
            self.assertEqual(dialog.call_count, 2)

    def test_installer_uses_each_users_home_and_keeps_existing_domain(self):
        with tempfile.TemporaryDirectory() as directory:
            home = Path(directory)
            env = {**os.environ, "HOME": directory, "USER": "differentuser"}
            subprocess.run([sys.executable, str(INSTALLER), "--domain", "other.example"],
                           env=env, check=True, capture_output=True)
            script = home / ".local/bin/halatha-factotum-login.py"
            launcher = home / ".config/autostart/halatha-factotum.desktop"
            domain_file = home / ".config/halatha/factotum-domain"
            self.assertEqual(script.read_bytes(), SCRIPT.read_bytes())
            self.assertIn(str(script), launcher.read_text())
            self.assertNotIn(str(SCRIPT), launcher.read_text())
            self.assertEqual(domain_file.read_text(), "other.example\n")
            subprocess.run([sys.executable, str(INSTALLER)], env=env, check=True, capture_output=True)
            self.assertEqual(domain_file.read_text(), "other.example\n")


if __name__ == "__main__":
    unittest.main()
