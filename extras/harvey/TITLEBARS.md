Halatha enables the `plan9front` editable title bars in Harvey's Rio with `-t`.
The boot environment supplies user and system names for the status label.

The port needed corrections in Harvey's `sys/src/cmd/rio/ui1.c` and `wind.c`:
file buffers no longer feed edited lines to the attached rc process or receive
its output; opening a file selects Text mode; cancelling title input restores
the previous mode. A failed save preserves the buffer and dirty state for a
retry. The path-directory helper also now allocates space for its terminator.

Run `node dev/test-rio-titlebars.mjs` against the guest served on port 7072.
The test uses real keyboard/mouse input, checks file contents through the host
9P gateway, and writes screenshots to `/tmp/rio-titlebars-*.png`.

The reference implementation is under
`/home/scott/Repo/plan9front/sys/src/cmd/rio`. Its `mkfile` builds `inf`.
Harvey's adapted copy is under `/home/scott/Repo/harvey/sys/src/cmd/rio` and is
built as `/bin/rio`. The adaptations are recorded in that directory's
`9FRONT-SYNC.md`; preserve them when importing further fixes.

| Behavior | Reference implementation | How it works |
| --- | --- | --- |
| Enable bars | `rio.c`, argument `-t` | Sets global `title`; `wmk` copies it into each window's `enable_title`. |
| Reserve bar space | `wind.c`, `wtagresize` | Creates a subordinate `Window` with its own text frame and undo stack, sharing the parent image. Moves the content rectangle below the bar. |
| Draw status | `wind.c`, `wtagrepaint` | Draws directory/file or prompt text on the left, mode and `user@sysname` on the right, and a modified-file `*`. An active input bar gets a black outline. |
| Drag or edit | `rio.c`, `mousethread`; `wind.c`, `wselect` | A left-button title drag moves the window. Double-click handling enters title text selection/editing. |
| Open/save | `ui1.c`, `fopen`, `fsave`, `self_insert` | The bar becomes an editable path. Enter performs the operation; Escape leaves input mode. |
| Search/history/prompts | `ui1.c`, `find`, `filehistory`, `cmdhistory`, `prompt` | Reuses the bar for search, history selection, completion and callback-driven prompts. |
| Key bindings | `ui1.c`, `key_map` | Includes Ctrl+O open, Ctrl+S save, Ctrl+F find, Ctrl+M file history and Ctrl+N command history. |

The current browser path is:

```text
basic-v64-aspera.html → Harvey draw/mouse/keyboard → /bin/rio
                                                    └─ editable title bars
```

`extras/harvey/build_aspera.py` already builds the adapted Rio sources and
links Harvey's draw/frame/thread/plumb libraries. It includes the compatibility
helpers and recompiles completion support. This is a user-space feature; the
source inspection does not indicate a new kernel interface is needed.

The concrete activation change is in `extras/harvey/aspera_init.o9`:

```c
char *rargv[] = {"rio", "-t", "-s", "-i", "rc /lib/aspera/start.rc", nil};
```

Rebuild and validate the actual Rio boot with a newly created shell window:

1. Check that the bar reserves space, displays path/status, and survives move,
   resize and focus changes without covering the content or scrollbar.
2. Check drag versus double-click selection and editing, then Enter and Escape.
3. Open a disposable text file using Ctrl+O, edit it, verify `*`, save with
   Ctrl+S, and read the file back. Also test failure paths without losing text.
4. Check search and explicit completion/history. Harvey's existing port
   deliberately disables per-keystroke asynchronous autosuggest; preserve that
   fix and the existing control-key translation.

The old `dev/test-aspera-v64-boot.mjs` expects the Objective-9C canvas bridge
(`window.aspera.display`). The current Rio page exposes `fs` and `setHostClip`
instead. That test cannot validate this boot path without being updated.
Likewise, the page currently stages a literal `mnt/shelf/view/protocol` file;
reading that file alone is no longer evidence that a shelf service was mounted.

There are two other renderers in the tree, distinct from the currently launched
Rio:

- `harvey/sys/src/cmd/aspera/aspera_live.c` is the native C fallback that the
  current build stages as `/bin/aspera-live`. It has themed title text/buttons,
  but does not contain Rio's editable title-frame state. Porting the behavior
  there would require a title-input model, text selection/caret rendering,
  command routing and shared layout/hit-test geometry.
- `harvey/sys/src/cmd/o9aspera` is the earlier Objective-9C compositor. For that
  implementation, window-owned title state belongs in `window.o9`, layout and
  hit testing in `theme.o9`, drawing in `canvas.o9`, and input routing in
  `live.o9`. Notes must expose path/dirty/save state and terminal prompts must
  dispatch to the terminal. ShelfViews is not the owner of these per-window
  controls. Its earlier browser input bridge also needs Escape, navigation keys
  and selection support, and its fixed ASCII glyph rendering does not provide
  the reference implementation's Rune/font behavior.

For either custom compositor, first port the path/status/dirty display, then
editable path with Enter/Escape and open/save, and finally search/history and
completion. Reuse the reference behavior and the existing Harvey compatibility
fixes; copying the entire editor-bearing Rio `Window` into a compositor would
also import its terminal, file-buffer, undo and completion assumptions.
