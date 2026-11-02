// The bochs VGA adapter ships as a separate plugin script that registers
// itself in globalThis.V86GraphicsAdapters. Import it for that side effect:
// the module is loaded from a Blob URL in the v86 worker, so v86 cannot
// resolve v86-bochs-vga.js relative to import.meta.url at runtime.
import "./v86-bochs-vga.js";
import { BrowserWisp } from "./wisp-browser.js";

// The Wisp adapter can run its relay side in this worker; see wisp-browser.js.
globalThis.__wanixWispLocal = BrowserWisp;
// lib/libv86.mjs is the vendored v64 build (same file as ../v64/lib/libv86.mjs).
// It provides x86_64 + multiboot, ring9p, and the 2025 screen/9P interfaces
// that the older Wanix bundle predates. Keep the two copies in sync.
export { V86 } from "./libv86.mjs";
export { OffscreenScreenAdapter } from "./offscreen.js";
export { WanixHandle } from "../../api/handle.js";
