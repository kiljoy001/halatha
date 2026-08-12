//go:build !js && !wasm

package main

import (
	"io"
	"log"
	"os"
	"strings"
	"time"

	"tractor.dev/toolkit-go/engine/cli"
	"tractor.dev/wanix"
	"tractor.dev/wanix/fs"
	"tractor.dev/wanix/fs/net/tcp"
	"tractor.dev/wanix/native"
	"tractor.dev/wanix/wisp"
)

// wispCmd boots a minimal native wanix kernel (TaskFS + namespace with /net/tcp
// bound) and runs the WISP relay as a #task/<id> via the wisp TaskDriver. The
// browser points WISP_RELAY (or ?relay=) at the printed wisp:// URL; the relay
// dials real outbound TCP for the guest (Yggdrasil peers, etc.).
func wispCmd() *cli.Command {
	var listenAddr string
	cmd := &cli.Command{
		Usage: "wisp",
		Short: "run the WISP WebSocket->TCP relay as a wanix task",
		Run: func(ctx *cli.Context, args []string) {
			log.SetFlags(log.Ltime | log.Lmicroseconds | log.Lshortfile)

			taskfs := wanix.NewTaskFS()
			taskfs.Register("native", &native.ExecDriver{})
			taskfs.Register("wisp", &wisp.Driver{})

			root, err := wanix.NewRootWithTasks(taskfs)
			if err != nil {
				log.Fatal(err)
			}

			// Bind the (previously unused) Plan 9-style /net/tcp service into
			// the native namespace as net/tcp (wanix namespace paths are
			// relative, like hostexport's "#term"), giving the kernel a real-TCP
			// capability for other tasks/guest use. The wisp relay itself dials
			// via net directly (see wisp/driver.go).
			if err := root.NS().Bind(tcp.New(), ".", "net/tcp"); err != nil {
				log.Fatal("bind net/tcp:", err)
			}

			// Allocate the wisp task and set its cmd (Arg(0)) to the listen
			// address so wisp.Driver.Start picks it up. Writing the "cmd" file
			// shlex-splits into the task's args (task.go cmd FieldFile setter).
			task, err := taskfs.Alloc("wisp", root)
			if err != nil {
				log.Fatal("alloc wisp task:", err)
			}
			if err := writeFile(task, "cmd", listenAddr); err != nil {
				log.Fatal("set wisp cmd:", err)
			}
			if err := task.Start(); err != nil {
				log.Fatal("start wisp:", err)
			}

			log.Printf("wisp task #%s started; relay URL above. Ctrl-C to stop.", task.ID())

			// Block until the relay task exits (its exit file is written when
			// the http.Server stops) or the process is signaled.
			exitCode := waitForTaskExit(task)
			os.Exit(exitCode)
		},
	}
	cmd.Flags().StringVar(&listenAddr, "listen", wisp.DefaultListen, "addr to listen on (WISP WebSocket)")
	return cmd
}

// waitForTaskExit polls the task's exit file and returns its recorded code. The
// wisp driver writes "0"/"1" to #task/<id>/exit when the http.Server stops.
func waitForTaskExit(t *wanix.Task) int {
	for {
		f, err := t.Open("exit")
		if err == nil {
			b, _ := io.ReadAll(f)
			_ = f.Close()
			if s := strings.TrimSpace(string(b)); s != "" {
				if s == "0" {
					return 0
				}
				return 1
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func writeFile(t *wanix.Task, name, data string) error {
	f, err := t.Open(name)
	if err != nil {
		return err
	}
	if _, err := fs.Write(f, []byte(data)); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}
