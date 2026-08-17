import { WanixElement, parseNsAttribute } from "./base.js";
import { TaskElement } from "./task.js";

export class VMElement extends WanixElement {
    constructor() {
        super();
        this.rid = null;
        this.task = new TaskElement();
    }

    get path() {
        if (!this.rid) {
            throw new Error("VM not allocated");
        }
        return [this._vmpath, this.rid].join("/");
    }

    connectedCallback() {
        super.connectedCallback();

        this._screen = this.hasAttribute("screen");
        this.style.display = this._screen ? "block" : "contents";
        if (this._screen) {
            // VGA display surface: v86 renders into this canvas offscreen
            // (transferred to the worker); DOM input is forwarded over the
            // screen port. Size the <wanix-vm> element via CSS to scale it.
            this.canvas = document.createElement("canvas");
            this.canvas.width = 800;
            this.canvas.height = 600;
            this.canvas.style.width = "100%";
            this.canvas.style.height = "100%";
            this.canvas.style.display = "block";
            this.canvas.style.background = "#000";
            this.appendChild(this.canvas);
        }

        this.alias = this.getAttribute("alias") || this.getAttribute("id") || null;
        this.type = this.getAttribute("type") || "v86";
        this.ns = parseNsAttribute(this);
        this._term = this.hasAttribute("term");
        this._autostart = this.hasAttribute("start");

        const args = [];
        // other than export, these all map to qemu cli flags.
        // append is put in env because of shell quoting limitations atm
        const urlAttrs = new Set(["hda", "hdb", "fda", "fdb", "cdrom", "bios", "kernel", "initrd"]);
        [
            "export", "mem", "vga-mem", "hda", "hdb", "fda", "fdb", "cdrom",
            "boot", "bios", "acpi", "fastboot", "kernel", "initrd", "netdev", "relay", "virtfs",
            "webgl-screen",
        ].forEach((attr) => {
            if (this.hasAttribute(attr) || (attr === "webgl-screen" && new URLSearchParams(location.search).get("webgl-screen") === "1")) {
                let value = this.getAttribute(attr);
                if (attr === "webgl-screen") {
                    args.push(`-${attr}`);
                    return;
                }
                if (urlAttrs.has(attr)) {
                    value = normalizeVMURL(value);
                }
                args.push(`-${attr} ${value}`);
            }
        });

        this.task.type = "gojs";
        if (this.hasAttribute("append")) {
            this.task.env = `VM_APPEND=${this.getAttribute("append")}\n`;
        } else {
            this.task.env = "";
        }
        this.task.cmd = `#vm/${this.type}/${this.type}-vm.wasm ${args.join(" ")}`;
    }

    async _awake() {
        await this.allocate();
        if (this._autostart) {
            await this.start();
        }
    }

    async allocate() {
        if (this.rid) {
            throw new Error("VM already allocated");
        }
        this.rid = (await this._kernel.root.readText([this._vmpath, "new", this.type].join("/"))).trim();
        if (this.id) {
            await this._kernel.root.writeFile([this.path, "alias"].join("/"), this.id);
        }

        // Backing task is not in the DOM; wire kernel and ns manually.
        this.task._kernel = this._kernel;
        this.task._taskpath = this._taskpath;
        this.task._termpath = this._termpath;
        this.task.ns = this.ns;
        this.task.env += `vm=${this.rid}\n`;
        await this.task.allocate(this._childBinds());

        if (this._term) {
            const termID = (await this._kernel.root.readText([this._termpath, "new"].join("/"))).trim();
            this.term = [this._termpath, termID].join("/");
            await this._kernel.root.bind(this.term, [this.path, "term"].join("/"));
            if (this.alias) {
                await this._kernel.root.bind(this.term, [this._vmpath, this.alias, "term"].join("/"));
            }

            await this.task.taskRoot.bind(this.term, `${this._taskpath}/self/term`);

            const program = [this.term, "program"].join("/");
            await this.task.taskRoot.bind(program, [this.task.path, "fd/0"].join("/"));
            await this.task.taskRoot.bind(program, [this.task.path, "fd/1"].join("/"));
            await this.task.taskRoot.bind(program, [this.task.path, "fd/2"].join("/"));
        }
    }

    async start() {
        await this.task.start();
        if (this._screen) {
            await this._setupScreen();
        }
        console.log("vm started", this.rid, this.id);
    }

    // Hand a MessagePort to the v86 worker, then forward the transferred VGA
    // canvas and DOM input over it. The worker (v86/main.go) attaches its
    // screen adapter and input handlers when the port arrives.
    async _setupScreen() {
        const port = await this._connectScreenPort();
        if (!port) {
            console.warn("wanix-vm: v86 worker not ready, screen disabled");
            return;
        }
        this._screenPort = port;
        const offscreen = this.canvas.transferControlToOffscreen();
        this._screenPort.postMessage({ type: "canvas", canvas: offscreen }, [offscreen]);
        this._wireScreenInput();
    }

    async _connectScreenPort() {
        // _sendToWorker can find the task worker before the v86 program has
        // installed its message listener. Because MessagePorts are transferable,
        // use fresh channels until the v86 side answers the ping.
        for (let i = 0; i < 100; i++) {
            const ch = new MessageChannel();
            const ack = new Promise((resolve) => {
                const timer = setTimeout(() => resolve(false), 200);
                ch.port2.onmessage = (event) => {
                    if (event.data?.type === "ack") {
                        clearTimeout(timer);
                        resolve(true);
                    }
                };
            });

            if (this._kernel._sendToWorker(this.task.rid, { vmScreenPort: ch.port1 }, [ch.port1])) {
                ch.port2.postMessage({ type: "ping" });
                if (await ack) {
                    ch.port2.onmessage = null;
                    return ch.port2;
                }
            }

            ch.port2.close();
            await new Promise((r) => setTimeout(r, 50));
        }
        return null;
    }

    _wireScreenInput() {
        const port = this._screenPort;
        const send = (msg) => port.postMessage(msg);
        this.canvas.tabIndex = 0;
        this.canvas.style.outline = "none";
        this.canvas.style.cursor = "default";

        const buttons = (e) => [!!(e.buttons & 1), !!(e.buttons & 4), !!(e.buttons & 2)];
        this.canvas.addEventListener("mousedown", async (e) => {
            this.canvas.focus();
            e.preventDefault();
            await this._lockPointer();
            send({ type: "mouse-click", buttons: buttons(e) });
        });
        this.canvas.addEventListener("mouseup", (e) =>
            send({ type: "mouse-click", buttons: buttons(e) }));
        this.canvas.addEventListener("mousemove", (e) =>
            send({ type: "mouse-delta", delta: [e.movementX, -e.movementY] }));
        this.canvas.addEventListener("wheel", (e) => {
            e.preventDefault();
            send({ type: "mouse-wheel", delta: [e.deltaY, e.deltaX] });
        });
        this.canvas.addEventListener("keydown", (e) => {
            if (e.repeat) return;
            const sc = scancodesFor(e.code, true);
            if (sc) { e.preventDefault(); send({ type: "keyboard", scancodes: sc }); }
        });
        this.canvas.addEventListener("keyup", (e) => {
            const sc = scancodesFor(e.code, false);
            if (sc) { e.preventDefault(); send({ type: "keyboard", scancodes: sc }); }
        });

        document.addEventListener("pointerlockchange", () => {
            this.canvas.style.cursor = document.pointerLockElement === this.canvas ? "none" : "default";
        });
    }

    async _lockPointer() {
        if (document.pointerLockElement || !this.canvas.requestPointerLock) {
            return;
        }
        try {
            await this.canvas.requestPointerLock({ unadjustedMovement: true });
        } catch (err) {
            try {
                await this.canvas.requestPointerLock();
            } catch (_) {
                // Some browsers reject pointer lock in restricted contexts; the
                // normal non-locked mouse path still works while over the canvas.
            }
        }
    }
}

function normalizeVMURL(value) {
    if (!value || !isVMURL(value)) {
        return value;
    }
    return new URL(value, document.baseURI).href;
}

function isVMURL(value) {
    return value.includes("://") || value.startsWith("/") || value.startsWith("./") || value.startsWith("../");
}

// Set1 PS/2 scancodes keyed by KeyboardEvent.code. v86's keyboard-code bus
// carries Set1: make = sc, break = sc|0x80, extended = 0xE0 prefix.
const SCANCODES = {
    Escape: [0x01], Digit1: [0x02], Digit2: [0x03], Digit3: [0x04], Digit4: [0x05],
    Digit5: [0x06], Digit6: [0x07], Digit7: [0x08], Digit8: [0x09], Digit9: [0x0A],
    Digit0: [0x0B], Minus: [0x0C], Equal: [0x0D], Backspace: [0x0E], Tab: [0x0F],
    KeyQ: [0x10], KeyW: [0x11], KeyE: [0x12], KeyR: [0x13], KeyT: [0x14], KeyY: [0x15],
    KeyU: [0x16], KeyI: [0x17], KeyO: [0x18], KeyP: [0x19], BracketLeft: [0x1A],
    BracketRight: [0x1B], Enter: [0x1C], ControlLeft: [0x1D], KeyA: [0x1E], KeyS: [0x1F],
    KeyD: [0x20], KeyF: [0x21], KeyG: [0x22], KeyH: [0x23], KeyJ: [0x24], KeyK: [0x25],
    KeyL: [0x26], Semicolon: [0x27], Quote: [0x28], Backquote: [0x29], ShiftLeft: [0x2A],
    Backslash: [0x2B], KeyZ: [0x2C], KeyX: [0x2D], KeyC: [0x2E], KeyV: [0x2F], KeyB: [0x30],
    KeyN: [0x31], KeyM: [0x32], Comma: [0x33], Period: [0x34], Slash: [0x35], ShiftRight: [0x36],
    NumpadMultiply: [0x37], AltLeft: [0x38], Space: [0x39], CapsLock: [0x3A],
    F1: [0x3B], F2: [0x3C], F3: [0x3D], F4: [0x3E], F5: [0x3F], F6: [0x40], F7: [0x41],
    F8: [0x42], F9: [0x43], F10: [0x44], NumLock: [0x45], ScrollLock: [0x46],
    Numpad7: [0x47], Numpad8: [0x48], Numpad9: [0x49], NumpadSubtract: [0x4A],
    Numpad4: [0x4B], Numpad5: [0x4C], Numpad6: [0x4D], NumpadAdd: [0x4E],
    Numpad1: [0x4F], Numpad2: [0x50], Numpad3: [0x51], Numpad0: [0x52], NumpadDecimal: [0x53],
    F11: [0x57], F12: [0x58],
    // Extended (0xE0-prefixed)
    ControlRight: [0xE0, 0x1D], AltRight: [0xE0, 0x38], NumpadEnter: [0xE0, 0x1C],
    NumpadDivide: [0xE0, 0x35], ArrowUp: [0xE0, 0x48], ArrowDown: [0xE0, 0x50],
    ArrowLeft: [0xE0, 0x4B], ArrowRight: [0xE0, 0x4D], Home: [0xE0, 0x47], End: [0xE0, 0x4F],
    PageUp: [0xE0, 0x49], PageDown: [0xE0, 0x51], Insert: [0xE0, 0x52], Delete: [0xE0, 0x53],
    OSLeft: [0xE0, 0x5B], OSRight: [0xE0, 0x5C], ContextMenu: [0xE0, 0x5D],
};

function scancodesFor(code, down) {
    const mk = SCANCODES[code];
    if (!mk) return null;
    if (down) return mk;
    if (mk.length === 1) return [mk[0] | 0x80];
    return [mk[0], mk[mk.length - 1] | 0x80]; // extended: 0xE0, sc|0x80
}

if (typeof window !== "undefined") {
    customElements.define("wanix-vm", VMElement);
}
