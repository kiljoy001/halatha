//go:build js && wasm

package main

import (
	"flag"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
)

func parseFlags(args []string) (map[string]any, error) {
	var (
		// Memory options
		mem string

		// Storage options
		hda   string
		hdb   string
		fda   string
		fdb   string
		cdrom string

		// Boot options
		boot     string
		bios     string
		acpi     bool
		fastboot bool

		// Kernel boot options
		kernel string
		initrd string
		append string

		// Network options
		netdev string
		relay  string

		// VirtFS options
		virtfs string

		// Export device
		export      string
		webglScreen bool
	)
	f := flag.NewFlagSet("v86", flag.ContinueOnError)
	f.StringVar(&export, "export", "", "Set device to exporthost over")
	f.StringVar(&mem, "m", "512M", "Set memory size")
	f.StringVar(&mem, "mem", "512M", "Set memory size")
	f.StringVar(&hda, "hda", "", "Primary hard disk image")
	f.StringVar(&hdb, "hdb", "", "Secondary hard disk image")
	f.StringVar(&fda, "fda", "", "Floppy disk A image")
	f.StringVar(&fdb, "fdb", "", "Floppy disk B image")
	f.StringVar(&cdrom, "cdrom", "", "CD-ROM image")
	f.StringVar(&boot, "boot", "c", "Boot order (a,b,c,d,n)")
	f.StringVar(&kernel, "kernel", "", "Linux kernel image (bzImage)")
	f.StringVar(&initrd, "initrd", "", "Initial ramdisk image")
	f.StringVar(&append, "append", "", "Kernel command line")
	f.StringVar(&bios, "bios", "", "BIOS image file")
	f.BoolVar(&acpi, "acpi", false, "Enable ACPI")
	f.BoolVar(&fastboot, "fastboot", false, "Enable fast boot")
	f.StringVar(&netdev, "netdev", "", "Network device type (e.g. ne2k, virtio)")
	f.StringVar(&relay, "relay", "", "Network relay URL (e.g. wisp://host:port for real TCP)")
	f.StringVar(&virtfs, "virtfs", "", "VirtFS configuration")
	f.BoolVar(&webglScreen, "webgl-screen", false, "Use WebGL for the v86 VGA screen adapter")
	if err := f.Parse(args); err != nil {
		return nil, err
	}
	if os.Getenv("VM_APPEND") != "" {
		append = os.Getenv("VM_APPEND")
	}
	memorySize, err := parseMemorySize(mem)
	if err != nil {
		return nil, err
	}

	// Disk-boot mode boots a whole OS image (e.g. 9front) from hda/cdrom/fda
	// via the BIOS, instead of a Linux bzImage with a synthesized command
	// line. It's selected when no kernel image is given but a disk image is.
	diskBoot := kernel == "" && (hda != "" || hdb != "" || fda != "" || fdb != "" || cdrom != "")

	cfg := map[string]any{
		"memory_size":     memorySize,
		"vga_memory_size": 8 * 1024 * 1024, // 8MB
	}
	if diskBoot {
		// Keep serial0 available for boot/console chatter; Halatha's host 9P
		// bridge uses COM2 (/dev/eia1) so the 9P byte stream stays clean.
		cfg["uart1"] = true
	}
	if webglScreen {
		cfg["_webgl_screen"] = true
	}

	// Disk / BIOS options. URLs become v86 async range-loaded images; bare
	// namespace paths are left as strings for main.go to read into buffers.
	if hda != "" {
		cfg["hda"] = diskOption(hda)
	}
	if hdb != "" {
		cfg["hdb"] = diskOption(hdb)
	}
	if fda != "" {
		cfg["fda"] = diskOption(fda)
	}
	if fdb != "" {
		cfg["fdb"] = diskOption(fdb)
	}
	if cdrom != "" {
		cfg["cdrom"] = diskOption(cdrom)
	}
	if bios != "" {
		cfg["bios"] = diskOption(bios)
	}
	if boot != "" {
		cfg["boot_order"] = parseBootOrder(boot)
	}
	// Some non-Linux guests (9front in particular) require ACPI to boot under
	// v86, but older Plan 9 images can be less tolerant. Leave this explicit.
	if acpi {
		cfg["acpi"] = true
	}

	if netdev != "" || relay != "" {
		if netdev == "" {
			if diskBoot {
				netdev = "ne2k"
			} else {
				netdev = "virtio"
			}
		}
		// v86 network settings live under net_device. Older guests like
		// 9legacy need ne2k; Linux usually wants virtio.
		net := map[string]any{"type": netdev}
		if relay != "" {
			// Network relay for real outbound TCP from the browser. Use the
			// wisp:// or wisps:// scheme for a WISP WebSocket->TCP relay, or
			// fetch for v86's browser fetch backend.
			net["relay_url"] = relay
		}
		cfg["net_device"] = net
	}
	if virtfs != "" {
		cfg["filesystem"] = parseVirtfs(virtfs)
	}

	if !diskBoot {
		// Linux bzImage path: synthesize the kernel command line and default
		// to host9p as the root filesystem. The actual handle9p filesystem is
		// wired up by main.go from the worker.p9 channel.
		cmdline := "console=hvc0 loglevel=3 init=/bin/init"
		if append != "" {
			cmdline += " " + append
		}
		if export != "" {
			cmdline += " export=" + export
		}
		// mem=1008M memmap=16M$1008M
		if virtfs == "" {
			fsargs := []string{
				"rw",
				"root=host9p",
				"rootfstype=9p",
				"rootflags=trans=virtio,version=9p2000.L,aname=,cache=none,msize=131072",
			}
			cmdline += " " + strings.Join(fsargs, " ")
			// up to caller to set up filesystem (handle9p)
		}
		cfg["cmdline"] = cmdline
		if kernel != "" {
			cfg["kernel"] = diskOption(kernel)
		}
		if initrd != "" {
			cfg["initrd"] = diskOption(initrd)
		}
	}

	// Sentinel consumed by main.go to pick the boot path; not a v86 option.
	cfg["_diskboot"] = diskBoot
	return cfg, nil
}

// diskOption maps a -hda/-cdrom/... value to a v86 disk option. URLs (with a
// scheme, or absolute/relative paths) become async, range-loaded images so a
// large OS image isn't fetched up front; a bare wanix namespace path is left
// as a string for main.go to read into an in-memory buffer.
func diskOption(s string) any {
	if isURL(s) {
		return map[string]any{
			"url":       s,
			"async":     true,
			"use_parts": false,
		}
	}
	return s
}

// isURL reports whether s should be fetched by v86 rather than read from the
// wanix namespace. Wanix namespace paths never start with "/", ".", or a
// scheme.
func isURL(s string) bool {
	if strings.Contains(s, "://") {
		return true
	}
	return strings.HasPrefix(s, "/") || strings.HasPrefix(s, "./") || strings.HasPrefix(s, "../")
}

// parseMemorySize parses memory size strings like "512M", "1G", etc.
func parseMemorySize(sizeStr string) (int, error) {
	if sizeStr == "" {
		return 0, nil
	}

	re := regexp.MustCompile(`^(\d+(?:\.\d+)?)([KMGT]?)$`)
	matches := re.FindStringSubmatch(strings.ToUpper(sizeStr))
	if matches == nil {
		return 0, fmt.Errorf("invalid memory size format: %s", sizeStr)
	}

	value, err := strconv.ParseFloat(matches[1], 64)
	if err != nil {
		return 0, err
	}

	unit := matches[2]
	multipliers := map[string]float64{
		"":  1024 * 1024, // Default to MB if no unit
		"B": 1,
		"K": 1024,
		"M": 1024 * 1024,
		"G": 1024 * 1024 * 1024,
		"T": 1024 * 1024 * 1024 * 1024,
	}

	multiplier, ok := multipliers[unit]
	if !ok {
		multiplier = multipliers[""]
	}

	return int(value * multiplier), nil
}

// parseBootOrder converts a QEMU-style boot string (a=floppy, c=hard disk,
// d=cdrom, n=network) into v86's boot_order nibble encoding: three hex digits
// each selecting a device (1=floppy, 2=hard disk, 3=cdrom, 4=network), most
// significant first. v86 defaults like 0x312 (786) and 0x321 (801) use this.
func parseBootOrder(bootStr string) int {
	bootMap := map[byte]int{
		'a': 0x1, // Floppy
		'b': 0x1, // Floppy
		'c': 0x2, // Hard disk
		'd': 0x3, // CD-ROM
		'n': 0x4, // Network
	}
	if bootStr == "" {
		return 0x123 // floppy, hard disk, cdrom
	}
	first, ok := bootMap[bootStr[0]]
	if !ok {
		return 0x123
	}
	// Chosen device first, then fall back to hard disk then cdrom.
	return first<<8 | 0x23
}

// parseVirtfs parses VirtFS configuration
func parseVirtfs(virtfs string) map[string]any {
	if virtfs == "" {
		return nil
	}

	parts := strings.Split(virtfs, ",")
	if len(parts) < 2 {
		return nil
	}

	mode := parts[0]
	if mode != "proxy" {
		return nil
	}

	return map[string]any{
		"proxy_url": parts[1],
	}
}
