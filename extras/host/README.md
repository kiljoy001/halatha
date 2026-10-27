# Local factotum login prompt

Install the popup for the current desktop user:

```sh
python3 extras/host/install-factotum-login.py --domain your.auth.domain
```

The installer copies the script to `~/.local/bin/halatha-factotum-login.py`
and creates `~/.config/autostart/halatha-factotum.desktop`. Omit `--domain`
to enter the auth domain in a popup on first login. The domain is saved in
`~/.config/halatha/factotum-domain` and can be changed by editing that file
or running the installer again with `--domain`. `HALATHA_FACTOTUM_DOMAIN`
overrides the saved domain for one invocation. The account name comes from
`$USER` (with the current Unix account as a fallback).

At login, the script waits for the local factotum mount, asks for the password
in a masked Zenity dialog, and adds a `proto=dp9ik` key for that user and
domain to `~/mnt/factotum/ctl`. It expects factotum to be mounted there;
`HALATHA_FACTOTUM_CTL` can override the control file path.

The password stays in process memory and is not sent to secstore, passed as a
command argument, or written to a temporary file. Cancelling the dialog
leaves the desktop login alone. To reopen the prompt in the current session,
run:

```sh
python3 ~/.local/bin/halatha-factotum-login.py
```

In the browser guest, `auth/fgui` runs from `desktop.rc` and displays a Rio
prompt when factotum asks for a missing key.
