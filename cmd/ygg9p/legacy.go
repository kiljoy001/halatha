//go:build plan9

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

func stopLegacyExports() error {
	// The generic scanner retains a cached boot mount. Stop its note group so
	// neither old wildcard listener can reannounce; restart the scanner with
	// a fresh namespace after its service files have been disabled.
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return err
	}
	for _, e := range entries {
		if _, err := strconv.Atoi(e.Name()); err != nil {
			continue
		}
		path := filepath.Join("/proc", e.Name())
		data, err := os.ReadFile(filepath.Join(path, "args"))
		if err != nil {
			continue
		}
		args := strings.Fields(string(data))
		if len(args) != 4 || filepath.Base(args[0]) != "listen" || args[1] != "[tcp" || args[2] != "*" || args[3] != "/rc/bin/service]" {
			continue
		}
		ctl := filepath.Join(path, "ctl")
		st, err := os.Stat(ctl)
		if err != nil {
			return err
		}
		d, ok := st.Sys().(*syscall.Dir)
		if !ok || d.Uid != "none" {
			continue
		}
		// On 9front eve uses the group permission bits. procwstat permits eve to
		// add group-write even when the original process mode was 0640.
		old := st.Mode().Perm()
		if err := os.Chmod(ctl, old|0020); err != nil {
			return err
		}
		f, err := os.OpenFile(filepath.Join(path, "notepg"), os.O_WRONLY, 0)
		if err != nil {
			os.Chmod(ctl, old)
			return err
		}
		_, err = f.Write([]byte("kill"))
		f.Close()
		if err != nil {
			os.Chmod(ctl, old)
			return err
		}
		fmt.Println("Stopped legacy service scanner and wildcard listeners, pid", e.Name())
	}
	return nil
}
