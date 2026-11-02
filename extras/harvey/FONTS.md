# TrueType service on Harvey / Aspera

Stages 1–3 are integrated into the browser guest: Harvey has the copied
`libttf`, `truetypefs`, and matching libdraw font hooks. Boot mounts the font
service at `/n/ttf` before launching Rio. Clients use ordinary Plan 9 font
paths, for example `/n/ttf/Aspera.ttf.24/font`. Font parsing and shaping remain
in a user-space 9P service; the kernel does not parse TrueType files.

## Build and run

```sh
python3 extras/harvey/build_aspera.py
python3 -m http.server 7072 --bind 127.0.0.1
node dev/test-aspera-fonts.mjs
node dev/test-rio-titlebars.mjs
```

Open <http://127.0.0.1:7072/examples/basic-v64-aspera.html> and run `ttfcheck`
in the guest terminal to see two text sizes and a red test glyph. The colour
sample is drawn as an image; it does not imply that colour font strings work
through the kernel's existing character-drawing path.

The build requires the sibling Harvey sources containing this port. It builds
fresh `libttf-ttf.a` and `libdraw-ttf.a` in `local/aspera`, links `truetypefs`
and `ttfcheck`, and relinks Rio and its existing libframe against the updated
headers. It leaves Harvey's prebuilt global archives alone. `build_fonts.py`
can build just the font components; after changing libdraw, use the full
`build_aspera.py` build to relink Rio too.

The default font is the host's
`/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf`, staged as `Aspera.ttf` with
the included DejaVu licence. `--ttf-font /path/to/font.ttf` replaces it; the
regression probe expects DejaVu Sans's `ffi` ligature and mark anchors. Supply
the appropriate licence when distributing a replacement font. The build also
generates `ColourTest.ttf`, a deterministic COLRv1 fixture with a red `A`, from
the staged font. It requires a Unicode format-4 cmap and an `A` outline.
No font binaries are checked into this repository.

## Port details

The source came from the sibling plan9front working tree at HEAD
`f8034a765445cd677a01408a86afa1948d17112d`, including its local font changes.
This is not a claim that all these features are in stock upstream 9front.
Harvey's `sys/src/libttf/9FRONT-SYNC.md` records the adaptations.

Harvey's 32-bit draw protocol types are retained. The original seven-argument
`cachechars` API remains available for existing libframe callers; the new
string functions use a bounded internal entry point that tracks input runes
separately from output cache slots. Rebuild code that accesses `Font` or
`Cacheinfo` against the matching headers and archive.

`truetypefs` uses Harvey's libthread entry point and `threadpostmountsrv`.
The boot child shares the parent's namespace, mounts `/n/ttf`, and completes
before Rio starts. Clients inherit that mount. This avoids depending on a
named `/srv` publication and does not repair the separate ShelfViews service.

## Validation and limits

`ttfcheck` runs inside the real Harvey/v64 guest and checks outline pixels,
GSUB `ffi`, GPOS mark anchors, namespace visibility, font metrics at 24 and 36
pixels, combining-mark clusters, bounded widths, ligatures across cache
batches, rendered pixels, and agreement between drawing advance and width.
It also checks COLRv1 rasterization, red RGBA pixels served over 9P, and
libdraw's colour-capability cache. The browser test saves
`/tmp/aspera-fonts.png`. The Rio regression covers title editing/saving,
Backspace, Ctrl+U, forward deletion, PageUp/PageDown, and wheel scrolling.

Rio keeps its existing bitmap font. Cluster-aware libframe editing, the
kernel colour-character drawing patch, and the newer Rio font UI are later
stages. The imported shaper is partial, not a complete OpenType layout
engine. COLRv1 supports only its implemented paint formats; gradients use a
flat-colour fallback. This does not establish general emoji, all-script,
variable-font, or publishing-quality typography support.
