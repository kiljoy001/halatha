# Aspera on Halatha

`examples/basic-v64-aspera.html` boots Harvey's amd64 kernel in the v64 browser
CPU and launches the Objective-9C Aspera desktop. The guest paints the desktop,
Notes and terminal; the browser displays committed pixel buffers and forwards
keyboard and pointer events. No native VM runner is involved in this path.

Build the guest from the sibling Harvey and Objective-9C checkouts:

```sh
python3 extras/harvey/build_aspera.py
python3 -m http.server 7072 --bind 127.0.0.1
```

Open <http://127.0.0.1:7072/examples/basic-v64-aspera.html>.
Click the desktop to focus keyboard input. Alt+Enter opens a terminal,
Alt+1–4 switches workspaces, and Ctrl+S saves Notes.

The build requires the existing Harvey amd64 kernel, Objective-9C compiler,
clang, ld.lld, guest libraries, and native shell tools (`build/aspera-rc`,
`aspera-echo`, `aspera-test`, `aspera-ls`, `aspera-cat`). Pass
`--harvey-repo /path/to/harvey` if needed; `O9_REPO` selects Objective-9C.
The build writes only to this checkout's ignored `local/aspera` directory.
It does not modify the sibling source repositories or any GEFS disk image.

The browser's 9P2000 server serves executable assets read-only. Aspera's frame
control, full-frame and delta-tile buffers, input queue and clock use its
existing display protocol. Notes are stored in localStorage for this origin;
other guest runtime files are temporary. This is not a GEFS-backed boot.
Networking and other Halatha host services are not connected by this example.

`guest_compat.py` applies narrow compatibility corrections while building the
current Objective-9C sources: synchronous reply lifetime, actor message access
after replying, and launching shelf/shell children through libthread's
`procexec` rather than forking directly from a heap-backed actor stack.
`aspera_exec.c` supplies that launch adapter and waits up to 30 seconds for
`/srv/ShelfViews` after exec. Exec success precedes the service's class
registration and publication; the old client's one-second poll is insufficient
on the browser CPU. The build also restores the full canvas/Notes/shelf
readiness condition when the source contains the display-only debugging check.
These corrections need review
when updating the guest toolchain; unexpected runtime source shapes fail the
build instead of silently omitting the correction.

Validation:

```sh
node dev/test-harvey-9p.test.mjs
node dev/test-aspera-v64-boot.mjs
```

The browser test requires Playwright/Chromium and the HTTP server above. It
checks a frame published by the real guest, a command executed by its rc
terminal, `/mnt/shelf/view/protocol`, shelf menu clicks, and typing and saving
Notes through Ctrl+S to the guest file and browser storage. The screenshot is
written to `/tmp/halatha-aspera.png`.

The earlier serial-shell milestone remains at `basic-v64-harvey.html` and can
be staged with `python3 extras/harvey/mkroot.py`. Its regression test is
`node dev/test-harvey-v64-boot.mjs URL`.
