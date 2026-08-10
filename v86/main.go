//go:build js && wasm

package main

import (
	"embed"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"os"
	"sync"
	"syscall/js"

	_ "embed"

	"tractor.dev/wanix/misc/jsutil"
)

//go:embed lib.js
var assets embed.FS

func main() {
	log.SetFlags(log.Lshortfile)

	opts, err := parseFlags(os.Args[1:])
	if err != nil {
		log.Fatal(err)
	}

	// Disk-boot (e.g. 9front from an hda image) vs. the Linux bzImage path.
	diskBoot := false
	if v, ok := opts["_diskboot"]; ok {
		if b, _ := v.(bool); b {
			diskBoot = true
		}
		delete(opts, "_diskboot")
	}

	// Resolve disk/BIOS options given as namespace paths into in-memory
	// buffers. URL options are already v86 async range-loaded images.
	resolveDisks := func(keys ...string) {
		for _, k := range keys {
			v, ok := opts[k]
			if !ok {
				continue
			}
			s, isStr := v.(string)
			if !isStr {
				continue
			}
			buf, err := jsutil.AwaitErr(js.Global().Get("sys").Call("readFile", s))
			if err != nil {
				log.Fatalf("reading %s %s: %v", k, s, err)
			}
			opts[k] = buf
		}
	}
	resolveDisks("hda", "hdb", "fda", "fdb", "cdrom", "bios")

	var (
		pendingScreenPort js.Value
		handleScreenPort  func(js.Value)
	)
	js.Global().Get("self").Call("addEventListener", "message", js.FuncOf(func(this js.Value, args []js.Value) any {
		port := args[0].Get("data").Get("vmScreenPort")
		if !port.IsUndefined() {
			if handleScreenPort != nil {
				handleScreenPort(port)
			} else {
				pendingScreenPort = port
			}
		}
		return nil
	}))

	// NOTE: weird call Get on string error when file doesnt exist
	v86wasm, err := jsutil.AwaitErr(js.Global().Get("sys").Call("readFile", "#vm/v86/v86.wasm"))
	if err != nil {
		log.Fatal(err)
	}
	v86bios, err := jsutil.AwaitErr(js.Global().Get("sys").Call("readFile", "#vm/v86/seabios.bin"))
	if err != nil {
		log.Fatal(err)
	}
	v86vgabios, err := jsutil.AwaitErr(js.Global().Get("sys").Call("readFile", "#vm/v86/vgabios.bin"))
	if err != nil {
		log.Fatal(err)
	}
	// The Linux path loads a bzImage from the namespace; disk-boot guests
	// (9front) carry their own kernel on the disk image and don't need one.
	var bzImage js.Value
	if !diskBoot {
		bzImage, err = jsutil.AwaitErr(js.Global().Get("sys").Call("readFile", "boot/bzImage"))
		if err != nil {
			log.Fatal(err)
		}
	}

	// Dynamically import libv86.mjs by creating a Blob URL and using JS dynamic import.
	// Create a Blob from the embedded libv86 bytes
	jslib, err := assets.ReadFile("lib.js")
	if err != nil {
		log.Fatal(err)
	}
	jsbuf := js.Global().Get("Uint8Array").New(len(jslib))
	js.CopyBytesToJS(jsbuf, jslib)
	jslibBlob := js.Global().Get("Blob").New(
		[]any{"var thisWorker = self; var process = undefined;", jsbuf},
		map[string]any{"type": "application/javascript"},
	)
	jslibURL := js.Global().Get("URL").Call("createObjectURL", jslibBlob)

	jsmod, err := jsutil.AwaitErr(js.Global().Call("eval", "(url)=>import(url)").Invoke(jslibURL))
	if err != nil {
		log.Fatalf("failed to import lib.js: %v", err)
	}

	wasmBlob := js.Global().Get("Blob").New([]any{v86wasm}, map[string]any{"type": "application/wasm"})
	wasmURL := js.Global().Get("URL").Call("createObjectURL", wasmBlob)

	bufObj := func(buf js.Value) map[string]any {
		return map[string]any{
			"buffer": buf.Call("slice").Get("buffer"),
		}
	}

	// 9P handler for the v86 emulator. v86's handle9p adapter dispatches
	// requests concurrently and correlates replies by 9P tag (see Wc in
	// lib.js), so we keep a tag-keyed map of per-request reply callbacks.
	// 9P header layout: size[4] type[1] tag[2] -> tag is at offset 5.
	readTag := func(buf js.Value) uint16 {
		return uint16(buf.Index(5).Int()) | uint16(buf.Index(6).Int())<<8
	}
	var (
		p9Mu        sync.Mutex
		p9Callbacks = make(map[uint16]js.Value)
	)
	js.Global().Get("worker").Get("p9").Set("onmessage", js.FuncOf(func(this js.Value, args []js.Value) any {
		data := args[0].Get("data")
		tag := readTag(data)
		p9Mu.Lock()
		cb, ok := p9Callbacks[tag]
		if ok {
			delete(p9Callbacks, tag)
		}
		p9Mu.Unlock()
		if ok {
			cb.Invoke(data)
		}
		// Unknown tag: response after Tflush or duplicate -- silently drop.
		return nil
	}))
	p9handler := js.FuncOf(func(this js.Value, args []js.Value) any {
		req := args[0]
		cb := args[1]
		tag := readTag(req)
		p9Mu.Lock()
		if _, exists := p9Callbacks[tag]; exists {
			jsutil.Log("p9 tag collision on tag", tag)
		}
		p9Callbacks[tag] = cb
		p9Mu.Unlock()
		js.Global().Get("worker").Get("p9").Call("postMessage", req)
		return nil
	})

	if !diskBoot {
		if _, ok := opts["net_device"]; !ok {
			opts["net_device"] = map[string]any{
				"type": "virtio",
			}
		}
		if _, ok := opts["filesystem"]; !ok {
			opts["filesystem"] = map[string]any{
				"handle9p": p9handler,
			}
		}
	} else {
		if _, ok := opts["net_device"]; !ok {
			opts["net_device"] = map[string]any{
				"type": "none",
			}
		}
	}

	defaults := map[string]any{
		"autostart":       true,
		"wasm_path":       wasmURL,
		"bios":            bufObj(v86bios),
		"vga_bios":        bufObj(v86vgabios),
		"disable_speaker": true,
	}
	if !diskBoot {
		defaults["bzimage"] = bufObj(bzImage)
		defaults["bzimage_initrd_from_filesystem"] = false
		defaults["disable_mouse"] = true
		defaults["disable_keyboard"] = false
		defaults["virtio_console"] = true
	} else {
		// Graphical guest (e.g. 9front rio): keep pointer + keyboard enabled so
		// the host <wanix-screen> can forward DOM input over worker.screen.
		defaults["disable_mouse"] = false
		defaults["disable_keyboard"] = false
	}
	for k, v := range defaults {
		opts[k] = v
	}

	vm := jsmod.Get("V86").New(opts)

	// exportch carries the guest->host 9P export (Linux hostexport path only).
	var exportch js.Value
	var sendStdin func()

	// Screen setup. A graphical disk-boot guest renders to a canvas
	// transferred from the host <wanix-screen> (worker.screen); otherwise we
	// render offscreen (Linux console comes over virtio-console, not VGA).
	var (
		screenCanvas js.Value
		screenReady  bool
	)
	setupScreen := func(canvas js.Value) {
		screenAdapter := jsmod.Get("OffscreenScreenAdapter").New(canvas, js.FuncOf(func(this js.Value, args []js.Value) any {
			vm.Get("v86").Get("cpu").Get("devices").Get("vga").Call("screen_fill_buffer")
			return nil
		}))
		vm.Get("v86").Get("cpu").Get("devices").Get("vga").Set("screen", screenAdapter)
		vm.Set("screen_adapter", screenAdapter)
	}

	if !diskBoot {
		exportch = js.Global().Get("MessageChannel").New()
		// Buffer 9p messages on virtio-console1 output and post complete messages to exportch.port1
		var (
			p9Buf     []byte
			p9MsgLen  int
			p9NeedLen = 4 // First 4 bytes of message is size (LE uint32)
			signaled  bool
		)
		// Run synchronously on the JS callback frame: there are no blocking
		// operations in the body, and spawning a goroutine per byte introduces
		// a race on the shared p9Buf/p9MsgLen accumulator (Go can interleave
		// goroutines at any syscall/js call, and v86 fires this listener once
		// per byte during a burst, so concurrent appends/resets corrupt frames).
		vm.Call("add_listener", "serial0-output-byte", js.FuncOf(func(this js.Value, args []js.Value) any {
			if !signaled {
				signaled = true
				exportch.Get("port1").Call("postMessage", args[0])
				return nil
			}
			b := args[0].Int()
			p9Buf = append(p9Buf, byte(b))

			// First 4 bytes encode the 9P message size (little-endian uint32).
			if len(p9Buf) == p9NeedLen {
				p9MsgLen = int(binary.LittleEndian.Uint32(p9Buf[:4]))
			}

			// Once a full message is accumulated, ship it over and reset.
			if p9MsgLen > 0 && len(p9Buf) == p9MsgLen {
				uint8arr := js.Global().Get("Uint8Array").New(p9MsgLen)
				js.CopyBytesToJS(uint8arr, p9Buf)
				exportch.Get("port1").Call("postMessage", uint8arr)
				p9Buf = p9Buf[:0]
				p9MsgLen = 0
			}
			return nil
		}))
		exportch.Get("port1").Set("onmessage", js.FuncOf(func(this js.Value, args []js.Value) any {
			vm.Call("serial_send_bytes", "0", args[0].Get("data"))
			return nil
		}))

		vm.Call("add_listener", "virtio-console0-output-bytes", js.FuncOf(func(this js.Value, args []js.Value) any {
			// ugh, these events are still one byte at a time?!?
			go func() {
				jsBuf := args[0]
				buf := make([]byte, jsBuf.Get("byteLength").Int())
				js.CopyBytesToGo(buf, jsBuf)
				fmt.Fprint(os.Stdout, string(buf))
			}()
			return nil
		}))

		sendStdin = func() {
			buf := make([]byte, 4096)
			for {
				n, err := os.Stdin.Read(buf)
				if n > 0 {
					jsBuf := js.Global().Get("Uint8Array").New(n)
					js.CopyBytesToJS(jsBuf, buf[:n])
					vm.Get("bus").Call("send", "virtio-console0-input-bytes", jsBuf)
				}
				if err != nil {
					if err != io.EOF {
						log.Println("stdin read error:", err)
					}
					break
				}
			}
		}
	} else {
		// Disk-boot (9front): the host <wanix-vm screen> element posts a
		// MessagePort here (vmScreenPort) via the kernel's _sendToWorker, then
		// forwards the transferred VGA canvas and DOM input over it. Attach the
		// handler when the port arrives -- it may not exist yet at worker start.
		handleScreen := func(port js.Value) {
			port.Set("onmessage", js.FuncOf(func(this js.Value, args []js.Value) any {
				data := args[0].Get("data")
				switch data.Get("type").String() {
				case "ping":
					port.Call("postMessage", map[string]any{"type": "ack"})
				case "canvas":
					screenCanvas = data.Get("canvas")
					if screenReady {
						setupScreen(screenCanvas)
					}
				case "keyboard":
					vm.Call("keyboard_send_scancodes", data.Get("scancodes"))
				case "mouse-click":
					vm.Get("bus").Call("send", "mouse-click", data.Get("buttons"))
				case "mouse-delta":
					vm.Get("bus").Call("send", "mouse-delta", data.Get("delta"))
				case "mouse-wheel":
					vm.Get("bus").Call("send", "mouse-wheel", data.Get("delta"))
				}
				return nil
			}))
		}
		handleScreenPort = handleScreen
		if !pendingScreenPort.IsUndefined() {
			handleScreen(pendingScreenPort)
		}

		// Host 9P2000 namespace bridge: 9legacy speaks 9P over COM2
		// (/dev/eia1, v86 serial1). COM1 carries boot/debug output on some
		// images, so keep the 9P byte stream off serial0.
		p9pPort := js.Global().Get("worker").Get("p9p")
		if !p9pPort.IsUndefined() {
			var (
				spMu      sync.Mutex
				spBuf     []byte
				spMsgLen  int
				spNeedLen = 4 // first 4 bytes of a 9P message is the LE uint32 size
			)
			vm.Call("add_listener", "serial1-output-byte", js.FuncOf(func(this js.Value, args []js.Value) any {
				spMu.Lock()
				spBuf = append(spBuf, byte(args[0].Int()))
				if len(spBuf) == spNeedLen {
					spMsgLen = int(binary.LittleEndian.Uint32(spBuf[:4]))
				}
				if spMsgLen > 0 && len(spBuf) == spMsgLen {
					arr := js.Global().Get("Uint8Array").New(spMsgLen)
					js.CopyBytesToJS(arr, spBuf)
					p9pPort.Call("postMessage", arr)
					spBuf = spBuf[:0]
					spMsgLen = 0
				}
				spMu.Unlock()
				return nil
			}))
			p9pPort.Set("onmessage", js.FuncOf(func(this js.Value, args []js.Value) any {
				vm.Call("serial_send_bytes", "1", args[0].Get("data"))
				return nil
			}))
		}
	}

	vm.Call("add_listener", "emulator-ready", js.FuncOf(func(this js.Value, args []js.Value) any {
		if !diskBoot {
			vm.Get("bus").Call("send", "virtio-console0-resize", []any{100, 100})
		} else {
			vm.Call("serial_set_carrier_detect", "1", true)
			vm.Call("serial_set_data_set_ready", "1", true)
			vm.Call("serial_set_clear_to_send", "1", true)
		}

		if !screenCanvas.IsUndefined() {
			setupScreen(screenCanvas)
		} else {
			setupScreen(js.Global().Get("OffscreenCanvas").New(800, 600))
		}
		screenReady = true

		if !diskBoot {
			go sendStdin()
			js.Global().Get("self").Call("postMessage", map[string]any{
				"vm":     os.Getenv("vm"),
				"export": exportch.Get("port2"),
			}, []any{exportch.Get("port2")})
		} else {
			js.Global().Get("self").Call("postMessage", map[string]any{
				"vm": os.Getenv("vm"),
			})
		}
		return nil
	}))

	select {}
}
