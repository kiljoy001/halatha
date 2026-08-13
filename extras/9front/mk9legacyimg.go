//go:build ignore

package main

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	sectorSize = 512
	plan9MBR   = 0x39
	dmDir      = 0x80000000
)

type layout struct {
	plan9Start uint32
	plan9Size  uint32
	mbrPartOff int
	fatStart   uint32
	fatEnd     uint32
	fatOffset  int64
	fatSize    int
	gzStart    uint32
	gzEnd      uint32
	gzOffset   int64
	gzSize     int
}

type archiveEntry struct {
	Path  string
	Mode  uint32
	UID   string
	GID   string
	Mtime uint32
	Data  []byte
}

type multiFlag []string

func (m *multiFlag) String() string {
	if m == nil {
		return ""
	}
	return strings.Join(*m, ",")
}

func (m *multiFlag) Set(value string) error {
	*m = append(*m, value)
	return nil
}

func main() {
	base := flag.String("base", "", "base 9legacy raw disk image")
	out := flag.String("out", "", "output patched raw disk image")
	serial := flag.String("serial9p", "", "Plan 9 386 serial9p binary to install")
	serialDev := flag.String("serial-dev", "/dev/eia0", "Plan 9 serial device used for the host 9P bridge")
	virtio9p := flag.Bool("virtio9p", false, "use the kernel virtio-9p (#9) host bridge (mount '#9/0') instead of the userspace serial9p poster; the devvirtio9p driver must already be built into the kernel image")
	srcdir := flag.String("srcdir", "extras/9front", "directory containing serial9p.c and mkfile")
	extract := flag.String("extract", "", "extract one archive path and exit")
	extractOut := flag.String("extract-out", "", "path for -extract output")
	list := flag.String("list", "", "list archive paths containing this substring and exit")
	growGZRootMiB := flag.Int("grow-gzroot-mib", 0, "grow the gzroot partition by this many MiB")
	menuDefault := flag.String("menu-default", "1280x768,1", "PLAN9.INI menudefault value, or empty to leave unchanged")
	keepSerialConsole := flag.Bool("keep-serial-console", false, "keep the console=0 b115200 serial-console line in PLAN9.INI (for QEMU serial-scripted build images); by default it is stripped for the v86 VGA console")
	usbBoot := flag.Bool("usb-boot", false, "set wanixusb=1 in PLAN9.INI [common] so termrc runs usbstart (usbd) at boot; requires the usbns HCI driver + USB userland (/386/bin/usb/usbd) in the image")
	yggAuto := flag.Bool("ygg", false, "install /rc/bin/halatha.local (from srcdir/halatha.local) to auto-start Yggdrasil at boot once the network is up, and bundle srcdir/yggdrasil.peers as /lib/yggdrasil.peers so public peers are auto-injected (yggdrasilctl addPeer) -- the node joins the global Yggdrasil mesh with no manual config. Also installs /rc/bin/ygg-up (from srcdir/ygg-up.rc) as an on-demand twin of the boot hook: run `ygg-up` by name to (re)start Yggdrasil without rebooting (there is no clipboard into the v86 terminal, so baking the script at a /rc/bin name is how you run it without retyping). Uses a bundled /lib/yggdrasil.conf if present (add one with -add src:/lib/yggdrasil.conf) else generates a fresh random one each boot. Requires /386/bin/yggdrasil in the image. Real TCP peers need the page launched with ?relay=wisp://...")
	o9dir := flag.String("o9", "../objective-9c", "path to the objective-9c repo (9legacy branch); bundles the prebuilt o9c toolchain -- o9c/o9c, libo9.a, o9.h, the o9build/o9proj/o9plumb/o9plumbbuild rc helpers, the o9 runtime sources, and the 14 stdlib .o9 modules -- at the paths `mk install` would use (/386/bin/o9c, /386/lib/libo9.a, /sys/include/o9.h, /rc/bin/o9*, /sys/src/cmd/o9_*, /sys/lib/o9/stdlib/*) so o9build works in the guest. The image already ships 8c/8l/8a/yacc/ar/mk, so generated C compiles+links in-guest. Pass -o9 '' to skip. Skipped with a warning if the path does not exist. The 9legacy branch carries the lib9p adaptation (9legacy lacks 9front's srvrelease/srvacquire; the facade codegen emits compat shims), so the bundled toolchain targets 9legacy, not just 9front.")
	var adds multiFlag
	var addTrees multiFlag
	flag.Var(&adds, "add", "add a host file as src:dst[:octal-mode]")
	flag.Var(&addTrees, "addtree", "add a host directory recursively as srcdir:dstdir")
	flag.Parse()

	if *base == "" {
		failf("-base is required")
	}

	img, entries, lay, err := loadImage(*base)
	if err != nil {
		failf("%v", err)
	}

	if *extract != "" {
		ent := findEntry(entries, *extract)
		if ent == nil {
			failf("%s not found in image", *extract)
		}
		if *extractOut == "" {
			if _, err := os.Stdout.Write(ent.Data); err != nil {
				failf("write stdout: %v", err)
			}
			return
		}
		if err := os.WriteFile(*extractOut, ent.Data, 0644); err != nil {
			failf("write %s: %v", *extractOut, err)
		}
		fmt.Printf("extracted %s -> %s (%d bytes)\n", *extract, *extractOut, len(ent.Data))
		return
	}
	if *list != "" {
		for _, ent := range entries {
			if strings.Contains(ent.Path, *list) {
				fmt.Printf("%s %o %s %s %d %d\n", ent.Path, ent.Mode, ent.UID, ent.GID, ent.Mtime, len(ent.Data))
			}
		}
		return
	}

	if *out == "" {
		failf("-out is required unless -extract is used")
	}
	if *serial == "" && !*virtio9p {
		failf("-serial9p is required unless -extract or -virtio9p is used")
	}

	if *growGZRootMiB != 0 {
		var err error
		img, lay, err = growGZRoot(img, lay, *growGZRootMiB)
		if err != nil {
			failf("grow gzroot: %v", err)
		}
	}
	if *virtio9p {
		if err := patchVirtio9pEntries(&entries); err != nil {
			failf("%v", err)
		}
	} else {
		if err := patchEntries(&entries, *serial, *srcdir, *serialDev); err != nil {
			failf("%v", err)
		}
	}
	if err := addPayloads(&entries, adds, addTrees); err != nil {
		failf("%v", err)
	}
	if *yggAuto {
		if err := installHalathaLocal(&entries, *srcdir); err != nil {
			failf("%v", err)
		}
	}
	if *o9dir != "" {
		if info, err := os.Stat(*o9dir); err != nil || !info.IsDir() {
			fmt.Fprintf(os.Stderr, "mk9legacyimg: -o9 %q not found; skipping o9c toolchain bundle\n", *o9dir)
		} else {
			if err := installO9c(&entries, *o9dir); err != nil {
				failf("%v", err)
			}
		}
	}

	archive := writeArchive(entries)
	blz, err := packBLZ(archive)
	if err != nil {
		failf("pack BLZ: %v", err)
	}
	gz, err := gzipBytes(blz)
	if err != nil {
		failf("gzip: %v", err)
	}

	header := []byte(fmt.Sprintf("gzfilesystem %d\n", len(gz)))
	if len(header)+len(gz) > lay.gzSize {
		failf("patched gzroot is %d bytes, exceeds partition size %d", len(header)+len(gz), lay.gzSize)
	}

	payload := make([]byte, lay.gzSize)
	copy(payload, header)
	copy(payload[len(header):], gz)
	copy(img[lay.gzOffset:lay.gzOffset+int64(lay.gzSize)], payload)
	if err := patchPlan9INI(img, lay, *menuDefault, *keepSerialConsole, *usbBoot); err != nil {
		failf("patch PLAN9.INI: %v", err)
	}

	if err := os.WriteFile(*out, img, 0644); err != nil {
		failf("write %s: %v", *out, err)
	}

	_, verifyEntries, _, err := loadImage(*out)
	if err != nil {
		failf("verify %s: %v", *out, err)
	}
	verifyPaths := []string{"/rc/bin/termrc"}
	if *virtio9p {
		verifyPaths = append(verifyPaths, "/rc/bin/wanixnet")
	} else {
		verifyPaths = append(verifyPaths, "/386/bin/serial9p", "/sys/src/wanix/serial9p.c")
	}
	if *yggAuto {
		verifyPaths = append(verifyPaths, "/rc/bin/halatha.local", "/386/bin/yggdrasil")
	}
	for _, p := range verifyPaths {
		if findEntry(verifyEntries, p) == nil {
			failf("verify %s: missing %s", *out, p)
		}
	}

	fmt.Printf("wrote %s\n", *out)
	fmt.Printf("gzroot: %d/%d bytes used\n", len(header)+len(gz), lay.gzSize)
	fmt.Printf("archive entries: %d\n", len(verifyEntries))
}

func loadImage(path string) ([]byte, []archiveEntry, layout, error) {
	img, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, layout{}, err
	}
	lay, err := findLayout(img)
	if err != nil {
		return nil, nil, layout{}, err
	}
	payload := img[lay.gzOffset : lay.gzOffset+int64(lay.gzSize)]
	blz, err := readGZFilesystem(payload)
	if err != nil {
		return nil, nil, layout{}, err
	}
	archive, err := unpackBLZ(blz)
	if err != nil {
		return nil, nil, layout{}, err
	}
	entries, err := parseArchive(archive)
	if err != nil {
		return nil, nil, layout{}, err
	}
	return img, entries, lay, nil
}

func findLayout(img []byte) (layout, error) {
	if len(img) < sectorSize {
		return layout{}, errors.New("image is smaller than one sector")
	}
	if img[510] != 0x55 || img[511] != 0xaa {
		return layout{}, errors.New("image does not have an MBR signature")
	}
	var plan9Start, plan9Size uint32
	mbrPartOff := -1
	for i := 0; i < 4; i++ {
		off := 446 + i*16
		if img[off+4] == plan9MBR {
			plan9Start = binary.LittleEndian.Uint32(img[off+8 : off+12])
			plan9Size = binary.LittleEndian.Uint32(img[off+12 : off+16])
			mbrPartOff = off
			break
		}
	}
	if plan9Start == 0 {
		return layout{}, errors.New("no Plan 9 MBR partition found")
	}
	if plan9Size == 0 {
		return layout{}, errors.New("Plan 9 MBR partition has zero size")
	}

	partOff := int64(plan9Start) * sectorSize
	if partOff < 0 || partOff+64*1024 > int64(len(img)) {
		return layout{}, errors.New("Plan 9 partition offset is outside image")
	}
	prep := string(img[partOff : partOff+64*1024])
	var fatStart, fatEnd uint32
	var gzStart, gzEnd uint32
	for _, line := range strings.Split(prep, "\n") {
		if idx := strings.Index(line, "part "); idx >= 0 {
			line = line[idx:]
		}
		fields := strings.Fields(line)
		if len(fields) < 4 || fields[0] != "part" {
			continue
		}
		start, err := strconv.ParseUint(fields[2], 10, 32)
		if err != nil {
			return layout{}, fmt.Errorf("bad %s start: %w", fields[1], err)
		}
		end, err := strconv.ParseUint(fields[3], 10, 32)
		if err != nil {
			return layout{}, fmt.Errorf("bad %s end: %w", fields[1], err)
		}
		switch fields[1] {
		case "9fat":
			fatStart = uint32(start)
			fatEnd = uint32(end)
		case "gzroot":
			gzStart = uint32(start)
			gzEnd = uint32(end)
		}
	}
	if fatEnd <= fatStart {
		return layout{}, errors.New("9fat partition not found in Plan 9 prep table")
	}
	if gzEnd <= gzStart {
		return layout{}, errors.New("gzroot partition not found in Plan 9 prep table")
	}

	fatOffset := int64(plan9Start+fatStart) * sectorSize
	fatSize := int(fatEnd-fatStart) * sectorSize
	if fatOffset < 0 || fatOffset+int64(fatSize) > int64(len(img)) {
		return layout{}, errors.New("9fat partition is outside image")
	}
	gzOffset := int64(plan9Start+gzStart) * sectorSize
	gzSize := int(gzEnd-gzStart) * sectorSize
	if gzOffset < 0 || gzOffset+int64(gzSize) > int64(len(img)) {
		return layout{}, errors.New("gzroot partition is outside image")
	}
	return layout{
		plan9Start: plan9Start,
		plan9Size:  plan9Size,
		mbrPartOff: mbrPartOff,
		fatStart:   fatStart,
		fatEnd:     fatEnd,
		fatOffset:  fatOffset,
		fatSize:    fatSize,
		gzStart:    gzStart,
		gzEnd:      gzEnd,
		gzOffset:   gzOffset,
		gzSize:     gzSize,
	}, nil
}

func growGZRoot(img []byte, lay layout, mib int) ([]byte, layout, error) {
	if mib < 0 {
		return nil, layout{}, errors.New("-grow-gzroot-mib cannot be negative")
	}
	if mib == 0 {
		return img, lay, nil
	}
	if lay.mbrPartOff < 0 {
		return nil, layout{}, errors.New("Plan 9 MBR partition offset is unavailable")
	}
	if lay.gzEnd > lay.plan9Size {
		return nil, layout{}, errors.New("gzroot extends beyond Plan 9 partition")
	}
	if lay.gzEnd != lay.plan9Size-32 {
		return nil, layout{}, fmt.Errorf("gzroot is not the final partition slice: gzroot end %d, Plan 9 size %d", lay.gzEnd, lay.plan9Size)
	}

	growSectors := uint32((mib * 1024 * 1024) / sectorSize)
	if growSectors == 0 {
		return nil, layout{}, fmt.Errorf("-grow-gzroot-mib=%d is too small", mib)
	}
	newGZEnd := lay.gzEnd + growSectors
	newPlan9Size := lay.plan9Size + growSectors
	if newGZEnd < lay.gzEnd || newPlan9Size < lay.plan9Size {
		return nil, layout{}, errors.New("grown partition size overflows")
	}

	oldEnd := strconv.FormatUint(uint64(lay.gzEnd), 10)
	newEnd := strconv.FormatUint(uint64(newGZEnd), 10)
	if len(newEnd) > len(oldEnd) {
		return nil, layout{}, fmt.Errorf("grown gzroot end %s does not fit existing prep field width %d", newEnd, len(oldEnd))
	}

	minLen := int64(lay.plan9Start+newPlan9Size) * sectorSize
	if minLen > int64(len(img)) {
		next := make([]byte, minLen)
		copy(next, img)
		img = next
	}

	partOff := int64(lay.plan9Start) * sectorSize
	prep := img[partOff : partOff+64*1024]
	needle := []byte(fmt.Sprintf("part gzroot %d %s", lay.gzStart, oldEnd))
	idx := bytes.Index(prep, needle)
	if idx < 0 {
		return nil, layout{}, fmt.Errorf("could not find prep line %q", string(needle))
	}
	endOff := idx + len(needle) - len(oldEnd)
	padded := fmt.Sprintf("%*s", len(oldEnd), newEnd)
	copy(prep[endOff:endOff+len(oldEnd)], padded)

	binary.LittleEndian.PutUint32(img[lay.mbrPartOff+12:lay.mbrPartOff+16], newPlan9Size)
	lay.plan9Size = newPlan9Size
	lay.gzEnd = newGZEnd
	lay.gzSize = int(newGZEnd-lay.gzStart) * sectorSize
	return img, lay, nil
}

func readGZFilesystem(payload []byte) ([]byte, error) {
	const prefix = "gzfilesystem "
	if !bytes.HasPrefix(payload, []byte(prefix)) {
		return nil, errors.New("gzroot does not start with gzfilesystem header")
	}
	nl := bytes.IndexByte(payload, '\n')
	if nl < 0 {
		return nil, errors.New("gzfilesystem header has no newline")
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(payload[len(prefix):nl])))
	if err != nil {
		return nil, fmt.Errorf("bad gzfilesystem length: %w", err)
	}
	start := nl + 1
	if n < 0 || start+n > len(payload) {
		return nil, errors.New("gzfilesystem length exceeds partition")
	}
	zr, err := gzip.NewReader(bytes.NewReader(payload[start : start+n]))
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	return io.ReadAll(zr)
}

func patchPlan9INI(img []byte, lay layout, menuDefault string, keepSerialConsole bool, usbBoot bool) error {
	fat := img[lay.fatOffset : lay.fatOffset+int64(lay.fatSize)]
	if len(fat) < sectorSize {
		return errors.New("9fat is too small")
	}
	bps := int(binary.LittleEndian.Uint16(fat[11:13]))
	spc := int(fat[13])
	reserved := int(binary.LittleEndian.Uint16(fat[14:16]))
	nfats := int(fat[16])
	rootEntries := int(binary.LittleEndian.Uint16(fat[17:19]))
	fatSectors := int(binary.LittleEndian.Uint16(fat[22:24]))
	if bps <= 0 || spc <= 0 || reserved <= 0 || nfats <= 0 || rootEntries <= 0 || fatSectors <= 0 {
		return errors.New("unsupported 9fat BPB")
	}

	rootSectors := (rootEntries*32 + bps - 1) / bps
	rootOff := (reserved + nfats*fatSectors) * bps
	rootLen := rootSectors * bps
	dataOffBase := rootOff + rootLen
	clusterSize := bps * spc
	if rootOff < 0 || rootOff+rootLen > len(fat) || dataOffBase > len(fat) {
		return errors.New("9fat root directory is outside partition")
	}

	for off := rootOff; off < rootOff+rootLen; off += 32 {
		if fat[off] == 0x00 {
			break
		}
		if fat[off] == 0xe5 || fat[off+11]&0x0f == 0x0f {
			continue
		}
		if string(fat[off:off+11]) != "PLAN9   INI" {
			continue
		}
		cluster := int(binary.LittleEndian.Uint16(fat[off+26 : off+28]))
		size := int(binary.LittleEndian.Uint32(fat[off+28 : off+32]))
		if cluster < 2 {
			return errors.New("PLAN9.INI has a bad first cluster")
		}
		fileOff := dataOffBase + (cluster-2)*clusterSize
		if fileOff < 0 || fileOff+clusterSize > len(fat) || size > clusterSize {
			return errors.New("PLAN9.INI spans unsupported clusters")
		}
		next := patchPlan9INIContents(fat[fileOff:fileOff+size], menuDefault, keepSerialConsole, usbBoot)
		if len(next) > clusterSize {
			return errors.New("patched PLAN9.INI exceeds its first cluster")
		}
		clear(fat[fileOff : fileOff+clusterSize])
		copy(fat[fileOff:fileOff+len(next)], next)
		binary.LittleEndian.PutUint32(fat[off+28:off+32], uint32(len(next)))
		return nil
	}
	return errors.New("PLAN9.INI not found in 9fat")
}

func withoutSerialConsole(data []byte) []byte {
	const drop = "console=0 b115200"
	var out bytes.Buffer
	for len(data) > 0 {
		nl := bytes.IndexByte(data, '\n')
		var line []byte
		if nl < 0 {
			line = data
			data = nil
		} else {
			line = data[:nl+1]
			data = data[nl+1:]
		}
		trimmed := bytes.TrimRight(line, "\r\n")
		if string(trimmed) == drop {
			continue
		}
		out.Write(line)
	}
	return out.Bytes()
}

func patchPlan9INIContents(data []byte, menuDefault string, keepSerialConsole bool, usbBoot bool) []byte {
	if !keepSerialConsole {
		data = withoutSerialConsole(data)
	} else {
		// -keep-serial-console is meant for QEMU serial-scripted builds, so
		// guarantee a COM1 serial console even when the base image was built
		// without one (console=0 b115200 stripped for the v86 VGA console).
		// withSectionSetting is a no-op if a console= line is already present.
		data = withSectionSetting(data, "common", "console", "0 b115200")
	}
	data = withMenuDefault(data, menuDefault)
	// nousbboot must be set WITHOUT a '*' prefix. A '*' prefix lands the
	// var in the kernel global env (#ec) as "*nousbboot" (main.c keeps the
	// '*'), but boot's getenv("nousbboot") reads /env/nousbboot from the
	// #e-over-#ec union — so the '*' form is never found, nousbboot stays
	// 0, usbinit runs, and boot prints "no /boot/usbd" (harmless but noisy;
	// only surfaced once usbns creates #u/usb/ctl, making usbinit proceed).
	// The bare form lands in #e/nousbboot where boot reads it. Convert any
	// stale '*' form left by older builds too.
	data = bytes.Replace(data, []byte("*nousbboot=1"), []byte("nousbboot=1"), 1)
	data = withSectionSetting(data, "common", "nousbboot", "1")
	if usbBoot {
		data = withSectionSetting(data, "common", "wanixusb", "1")
	}
	return data
}

func withMenuDefault(data []byte, value string) []byte {
	if value == "" {
		return data
	}
	line := []byte("menudefault=" + value)
	for start := 0; start <= len(data); {
		endRel := bytes.IndexByte(data[start:], '\n')
		end := len(data)
		next := len(data) + 1
		if endRel >= 0 {
			end = start + endRel
			next = end + 1
		}
		trimmed := bytes.TrimRight(data[start:end], "\r")
		if bytes.HasPrefix(trimmed, []byte("menudefault=")) {
			out := make([]byte, 0, len(data)-len(trimmed)+len(line))
			out = append(out, data[:start]...)
			out = append(out, line...)
			out = append(out, data[end:]...)
			return out
		}
		if next > len(data) {
			break
		}
		start = next
	}
	return withSectionLineEnd(data, "menu", string(line))
}

func withSectionSetting(data []byte, section, key, value string) []byte {
	prefix := []byte(key + "=")
	if bytes.HasPrefix(data, prefix) || bytes.Contains(data, append([]byte("\n"), prefix...)) {
		return data
	}
	return withSectionLine(data, section, key+"="+value)
}

func withSectionLine(data []byte, section, line string) []byte {
	header := "[" + section + "]"
	idx := bytes.Index(data, []byte(header))
	if idx < 0 {
		return data
	}
	nl := bytes.IndexByte(data[idx:], '\n')
	if nl < 0 {
		return data
	}
	insert := idx + nl + 1
	lineBytes := []byte(line + "\n")
	out := make([]byte, 0, len(data)+len(lineBytes))
	out = append(out, data[:insert]...)
	out = append(out, lineBytes...)
	out = append(out, data[insert:]...)
	return out
}

func withSectionLineEnd(data []byte, section, line string) []byte {
	header := "[" + section + "]"
	idx := bytes.Index(data, []byte(header))
	if idx < 0 {
		return data
	}
	startRel := bytes.IndexByte(data[idx:], '\n')
	if startRel < 0 {
		return data
	}
	insert := idx + startRel + 1
	for {
		next := bytes.IndexByte(data[insert:], '\n')
		if next < 0 {
			insert = len(data)
			break
		}
		lineStart := insert
		lineEnd := insert + next
		trimmed := bytes.TrimSpace(data[lineStart:lineEnd])
		if len(trimmed) > 0 && trimmed[0] == '[' {
			insert = lineStart
			break
		}
		insert = lineEnd + 1
	}
	lineBytes := []byte(line + "\n")
	out := make([]byte, 0, len(data)+len(lineBytes))
	out = append(out, data[:insert]...)
	out = append(out, lineBytes...)
	out = append(out, data[insert:]...)
	return out
}

type blzBlock struct {
	raw bool
	n   int
	off int
}

func unpackBLZ(p []byte) ([]byte, error) {
	if len(p) < 8 || string(p[:4]) != "BLZ\n" {
		return nil, errors.New("bad BLZ header")
	}
	total := int(binary.BigEndian.Uint32(p[4:8]))
	pos := 8
	sum := 0
	var blocks []blzBlock
	for sum < total {
		if pos+4 > len(p) {
			return nil, errors.New("short BLZ block table")
		}
		v := binary.BigEndian.Uint32(p[pos : pos+4])
		pos += 4
		if v&(1<<31) != 0 {
			n := int(v &^ (1 << 31))
			blocks = append(blocks, blzBlock{raw: true, n: n})
			sum += n
			continue
		}
		if pos+4 > len(p) {
			return nil, errors.New("short BLZ copy table")
		}
		n := int(v)
		off := int(binary.BigEndian.Uint32(p[pos : pos+4]))
		pos += 4
		blocks = append(blocks, blzBlock{n: n, off: off})
		sum += n
	}
	if sum != total {
		return nil, fmt.Errorf("bad BLZ length: table sums to %d, want %d", sum, total)
	}

	out := make([]byte, total)
	w := 0
	for _, b := range blocks {
		if b.n < 0 || w+b.n > len(out) {
			return nil, errors.New("BLZ block exceeds output")
		}
		if b.raw {
			if pos+b.n > len(p) {
				return nil, errors.New("short BLZ raw payload")
			}
			copy(out[w:w+b.n], p[pos:pos+b.n])
			pos += b.n
		} else {
			if b.off < 0 || b.off+b.n > w+b.n {
				return nil, errors.New("bad BLZ copy reference")
			}
			for i := 0; i < b.n; i++ {
				out[w+i] = out[b.off+i]
			}
		}
		w += b.n
	}
	return out, nil
}

func packBLZRaw(data []byte) ([]byte, error) {
	if len(data) >= 1<<31 {
		return nil, errors.New("archive too large for one raw BLZ block")
	}
	out := make([]byte, 12+len(data))
	copy(out[:4], "BLZ\n")
	binary.BigEndian.PutUint32(out[4:8], uint32(len(data)))
	binary.BigEndian.PutUint32(out[8:12], uint32(len(data))|(1<<31))
	copy(out[12:], data)
	return out, nil
}

type bflzNode struct {
	offset [3]int
}

type bflzBlockWriter struct {
	data     []byte
	blocks   bytes.Buffer
	raw      bytes.Buffer
	acct     int
	rawStart int
	nraw     int
}

func packBLZ(data []byte) ([]byte, error) {
	if len(data) >= 1<<31 {
		return nil, errors.New("archive too large for BLZ")
	}
	if len(data) == 0 {
		return packBLZRaw(data)
	}

	const (
		prime  = 16777213
		win    = 16
		minrun = 16
	)

	bw := &bflzBlockWriter{data: data}
	hash := make(map[uint32]*bflzNode, len(data)/8)
	maxRLE := [256]int{}
	outn := uint32(1)
	for i := 0; i < win; i++ {
		outn = (outn * 256) % prime
	}
	outn = prime - outn

	var sum uint32
	for i := 0; i < win && i < len(data); i++ {
		sum = (sum*256 + uint32(data[i])) % prime
	}

	insert := func(key uint32, off int) {
		n := hash[key]
		if n == nil {
			n = &bflzNode{offset: [3]int{-1, -1, -1}}
			hash[key] = n
		}
		slot := len(n.offset) - 1
		for i := 0; i < len(n.offset)-1; i++ {
			if n.offset[i] == -1 {
				slot = i
				break
			}
		}
		n.offset[slot] = off
	}

	i := 0
	for i < len(data)-win {
		maxRun := 0
		maxOff := 0
		best := -1
		if n := hash[sum]; n != nil {
			for o, off := range n.offset {
				if off == -1 {
					break
				}
				if off >= i {
					continue
				}
				run := commonRun(data[i:], data[off:])
				if run > maxRun {
					maxRun = run
					maxOff = off
					best = o
				}
			}
			if best > 0 {
				off := n.offset[best]
				for j := best; j > 0; j-- {
					n.offset[j] = n.offset[j-1]
				}
				n.offset[0] = off
			}
		}

		next := i + 1
		if maxRun >= minrun {
			if err := bw.refBlock(i, maxRun, maxOff); err != nil {
				return nil, err
			}
			next = i + maxRun
		} else if err := bw.rawByte(i); err != nil {
			return nil, err
		}

		for ; i < next && i < len(data)-win; i++ {
			rle := countRLE(data[i:])
			if rle < 4 {
				insert(sum, i)
			} else if rle > maxRLE[data[i]] {
				maxRLE[data[i]] = rle
				insert(sum, i)
			}
			sum = (sum*256 + uint32(data[i+win])) % prime
			sum = (sum + uint32(data[i])*outn) % prime
		}
		if i < next {
			i = next
		}
	}

	for i < len(data) {
		if err := bw.rawByte(i); err != nil {
			return nil, err
		}
		i++
	}
	if err := bw.flushRaw(); err != nil {
		return nil, err
	}

	var out bytes.Buffer
	out.Grow(8 + bw.blocks.Len() + bw.raw.Len())
	out.WriteString("BLZ\n")
	writeBE32(&out, uint32(len(data)))
	out.Write(bw.blocks.Bytes())
	out.Write(bw.raw.Bytes())
	return out.Bytes(), nil
}

func (w *bflzBlockWriter) rawByte(i int) error {
	if w.acct != i {
		return fmt.Errorf("internal BLZ raw accounting mismatch: have %d want %d", w.acct, i)
	}
	if w.nraw == 0 {
		w.rawStart = i
	}
	w.acct++
	w.nraw++
	return nil
}

func (w *bflzBlockWriter) refBlock(i, n, off int) error {
	if w.acct != i {
		return fmt.Errorf("internal BLZ ref accounting mismatch: have %d want %d", w.acct, i)
	}
	w.acct += n
	if err := w.flushRaw(); err != nil {
		return err
	}
	writeBE32(&w.blocks, uint32(n))
	writeBE32(&w.blocks, uint32(off))
	return nil
}

func (w *bflzBlockWriter) flushRaw() error {
	if w.nraw == 0 {
		return nil
	}
	if w.nraw >= 1<<31 {
		return errors.New("raw BLZ block too large")
	}
	writeBE32(&w.blocks, uint32(w.nraw)|(1<<31))
	w.raw.Write(w.data[w.rawStart : w.rawStart+w.nraw])
	w.nraw = 0
	return nil
}

func commonRun(a, b []byte) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	i := 0
	for i < n && a[i] == b[i] {
		i++
	}
	return i
}

func countRLE(data []byte) int {
	if len(data) == 0 {
		return 0
	}
	i := 1
	for i < len(data) && data[i] == data[0] {
		i++
	}
	return i
}

func writeBE32(w io.Writer, v uint32) {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], v)
	_, _ = w.Write(b[:])
}

func parseArchive(data []byte) ([]archiveEntry, error) {
	var entries []archiveEntry
	for len(data) > 0 {
		nl := bytes.IndexByte(data, '\n')
		if nl < 0 {
			return nil, errors.New("archive header without newline")
		}
		line := string(data[:nl])
		data = data[nl+1:]
		if line == "end of archive" {
			if len(data) != 0 {
				return nil, errors.New("trailing bytes after archive terminator")
			}
			return entries, nil
		}
		fields := splitArchiveHeader(line)
		if len(fields) != 6 {
			return nil, fmt.Errorf("bad archive header %q", line)
		}
		mode, err := strconv.ParseUint(fields[1], 8, 32)
		if err != nil {
			return nil, fmt.Errorf("%s: bad mode: %w", fields[0], err)
		}
		mtime, err := strconv.ParseUint(fields[4], 10, 32)
		if err != nil {
			return nil, fmt.Errorf("%s: bad mtime: %w", fields[0], err)
		}
		size, err := strconv.ParseUint(fields[5], 10, 32)
		if err != nil {
			return nil, fmt.Errorf("%s: bad size: %w", fields[0], err)
		}
		if uint64(len(data)) < size {
			return nil, fmt.Errorf("%s: archive data is short", fields[0])
		}
		ent := archiveEntry{
			Path:  fields[0],
			Mode:  uint32(mode),
			UID:   fields[2],
			GID:   fields[3],
			Mtime: uint32(mtime),
			Data:  append([]byte(nil), data[:size]...),
		}
		entries = append(entries, ent)
		data = data[size:]
	}
	return nil, errors.New("archive terminator not found")
}

func splitArchiveHeader(line string) []string {
	if !strings.HasPrefix(line, "'") {
		return strings.Fields(line)
	}
	var fields []string
	i := 1
	var path strings.Builder
	for i < len(line) {
		if line[i] == '\'' {
			if i+1 < len(line) && line[i+1] == '\'' {
				path.WriteByte('\'')
				i += 2
				continue
			}
			i++
			break
		}
		path.WriteByte(line[i])
		i++
	}
	fields = append(fields, path.String())
	fields = append(fields, strings.Fields(line[i:])...)
	return fields
}

func writeArchive(entries []archiveEntry) []byte {
	var out bytes.Buffer
	for _, ent := range entries {
		fmt.Fprintf(&out, "%s %o %s %s %d %d\n", ent.Path, ent.Mode, ent.UID, ent.GID, ent.Mtime, len(ent.Data))
		out.Write(ent.Data)
	}
	out.WriteString("end of archive\n")
	return out.Bytes()
}

func gzipBytes(data []byte) ([]byte, error) {
	var out bytes.Buffer
	zw, err := gzip.NewWriterLevel(&out, gzip.BestCompression)
	if err != nil {
		return nil, err
	}
	zw.Name = "gzroot"
	zw.ModTime = time.Unix(0, 0)
	if _, err := zw.Write(data); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func patchEntries(entries *[]archiveEntry, serialPath, srcdir, serialDev string) error {
	now := uint32(time.Now().Unix())
	if serialDev == "" {
		serialDev = "/dev/eia0"
	}
	serialLabel := strings.TrimPrefix(serialDev, "/dev/")
	serial, err := os.ReadFile(serialPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", serialPath, err)
	}
	if len(serial) == 0 {
		return fmt.Errorf("%s is empty", serialPath)
	}

	termrc := findEntry(*entries, "/rc/bin/termrc")
	if termrc == nil {
		return errors.New("/rc/bin/termrc not found")
	}
	termrc.Data = patchTermRCContents(termrc.Data)
	termrc.Data = bytes.ReplaceAll(termrc.Data, []byte("# wanix /n/host bridge over serial0"), []byte("# wanix /n/host bridge over "+serialLabel))
	termrc.Data = bytes.ReplaceAll(termrc.Data, []byte("# wanix /n/host bridge over serial1"), []byte("# wanix /n/host bridge over "+serialLabel))
	termrc.Data = bytes.ReplaceAll(termrc.Data, []byte("\tserial9p /dev/eia0 host &"), []byte("\tserial9p "+serialDev+" host &"))
	termrc.Data = bytes.ReplaceAll(termrc.Data, []byte("\tserial9p /dev/eia1 host &"), []byte("\tserial9p "+serialDev+" host &"))
	marker := "# wanix /n/host bridge over " + serialLabel
	if !bytes.Contains(termrc.Data, []byte(marker)) {
		block := "\n" + marker + "\n" +
			"if(! test -d /n)\n" +
			"\tmkdir /n\n" +
			"if(! test -d /n/host)\n" +
			"\tmkdir /n/host\n" +
			"if(test -x /bin/serial9p){\n" +
			"\tserial9p " + serialDev + " host &\n" +
			"\tfor(i in 1 2 3 4 5){\n" +
			"\t\tif(! test -e /srv/host)\n" +
			"\t\t\tsleep 1\n" +
			"\t}\n" +
			"\tif(test -e /srv/host)\n" +
			"\t\tmount -c /srv/host /n/host &\n" +
			"}\n"
		if len(termrc.Data) > 0 && termrc.Data[len(termrc.Data)-1] != '\n' {
			termrc.Data = append(termrc.Data, '\n')
		}
		termrc.Data = append(termrc.Data, []byte(block)...)
	}
	const hookMarker = "# halatha optional local boot hook"
	if !bytes.Contains(termrc.Data, []byte(hookMarker)) {
		block := "\n" + hookMarker + "\n" +
			"if(test -x /rc/bin/halatha.local)\n" +
			"\t/rc/bin/halatha.local &\n"
		if len(termrc.Data) > 0 && termrc.Data[len(termrc.Data)-1] != '\n' {
			termrc.Data = append(termrc.Data, '\n')
		}
		termrc.Data = append(termrc.Data, []byte(block)...)
	}

	upsert(entries, archiveEntry{
		Path:  "/386/bin/serial9p",
		Mode:  0775,
		UID:   "sys",
		GID:   "sys",
		Mtime: now,
		Data:  serial,
	})
	upsert(entries, archiveEntry{
		Path:  "/rc/bin/wanixnet",
		Mode:  0775,
		UID:   "sys",
		GID:   "sys",
		Mtime: now,
		Data:  []byte(wanixNetScript),
	})
	upsert(entries, archiveEntry{
		Path:  "/sys/src/wanix",
		Mode:  dmDir | 0775,
		UID:   "sys",
		GID:   "sys",
		Mtime: now,
	})
	for _, name := range []string{"serial9p.c", "mkfile"} {
		data, err := os.ReadFile(filepath.Join(srcdir, name))
		if err != nil {
			return fmt.Errorf("read %s: %w", name, err)
		}
		upsert(entries, archiveEntry{
			Path:  "/sys/src/wanix/" + name,
			Mode:  0664,
			UID:   "sys",
			GID:   "sys",
			Mtime: now,
			Data:  data,
		})
	}
	return nil
}

// patchVirtio9pEntries is the virtio-9p counterpart of patchEntries. Instead of
// embedding a userspace serial9p poster (which byte-feeds a UART and posts a
// /srv/host fd), it relies on the kernel #9 device (devvirtio9p, built into the
// kernel image supplied separately) and just appends a `mount -c '#9/0' /n/host`
// boot hook to termrc. The wire is plain 9P2000 end to end — the guest's
// devvirtio9p .u shim is compiled out (V9_9U undefined) and v86's
// Virtio9pHandler is a raw frame relay to the Go fs/p9p server — so no .u codec
// and no /srv fd posting are involved. The devvirtio9p driver must already be
// configured into the kernel image (-add/-addtree the rebuilt 9pcpu); this tool
// only patches the boot script.
func patchVirtio9pEntries(entries *[]archiveEntry) error {
	now := uint32(time.Now().Unix())

	termrc := findEntry(*entries, "/rc/bin/termrc")
	if termrc == nil {
		return errors.New("/rc/bin/termrc not found")
	}
	termrc.Data = patchTermRCContents(termrc.Data)
	termrc.Data = stripSerial9pBlock(termrc.Data)

	marker := "# wanix /n/host bridge over virtio-9p"
	if !bytes.Contains(termrc.Data, []byte(marker)) {
		block := "\n" + marker + "\n" +
			"if(! test -d /n)\n" +
			"\tmkdir /n\n" +
			"if(! test -d /n/host)\n" +
			"\tmkdir /n/host\n" +
			"mount -c '#9/0' /n/host\n"
		if len(termrc.Data) > 0 && termrc.Data[len(termrc.Data)-1] != '\n' {
			termrc.Data = append(termrc.Data, '\n')
		}
		termrc.Data = append(termrc.Data, []byte(block)...)
	}
	const hookMarker = "# halatha optional local boot hook"
	if !bytes.Contains(termrc.Data, []byte(hookMarker)) {
		block := "\n" + hookMarker + "\n" +
			"if(test -x /rc/bin/halatha.local)\n" +
			"\t/rc/bin/halatha.local &\n"
		if len(termrc.Data) > 0 && termrc.Data[len(termrc.Data)-1] != '\n' {
			termrc.Data = append(termrc.Data, '\n')
		}
		termrc.Data = append(termrc.Data, []byte(block)...)
	}

	upsert(entries, archiveEntry{
		Path:  "/rc/bin/wanixnet",
		Mode:  0775,
		UID:   "sys",
		GID:   "sys",
		Mtime: now,
		Data:  []byte(wanixNetScript),
	})
	return nil
}

// installHalathaLocal installs /rc/bin/halatha.local (the Yggdrasil boot hook,
// read from srcdir/halatha.local) so termrc's existing "# halatha optional
// local boot hook" block activates it, and bundles srcdir/yggdrasil.peers as
// /lib/yggdrasil.peers so halatha.local can auto-inject public peers at boot.
// Idempotent: prior installs are overwritten in place. The rc script lives in
// a real file (not a Go raw-string const) so it can use rc backtick command
// substitution to read the peers file.
func installHalathaLocal(entries *[]archiveEntry, srcdir string) error {
	now := uint32(time.Now().Unix())
	rc := filepath.Join(srcdir, "halatha.local")
	if err := addFile(entries, rc, "/rc/bin/halatha.local", 0, false, now); err != nil {
		return fmt.Errorf("install halatha.local: %w", err)
	}
	// ygg-up.rc is the on-demand twin of halatha.local: same Yggdrasil start
	// logic, runnable by name (`ygg-up`) to (re)start without rebooting. There
	// is no clipboard into the v86 terminal, so baking the script at a known
	// /rc/bin name is how you run it without retyping it each boot.
	yggUp := filepath.Join(srcdir, "ygg-up.rc")
	if _, err := os.Stat(yggUp); err == nil {
		if err := addFile(entries, yggUp, "/rc/bin/ygg-up", 0, false, now); err != nil {
			return fmt.Errorf("install ygg-up: %w", err)
		}
	}
	peers := filepath.Join(srcdir, "yggdrasil.peers")
	if _, err := os.Stat(peers); err == nil {
		if err := addFile(entries, peers, "/lib/yggdrasil.peers", 0, false, now); err != nil {
			return fmt.Errorf("install yggdrasil.peers: %w", err)
		}
	}
	return nil
}

// installO9c bundles the prebuilt objective-9c toolchain into the image at the
// paths `mk install` would use, so o9 apps can be built and run in the guest
// the moment it boots -- this is the distribution avenue for the language: boot
// the 9legacy VM and o9c is ready to use and develop with, no in-guest build.
// The image already ships the C toolchain (8c/8l/8a, yacc, ar, mk in /386/bin),
// so o9build can compile the generated C and link libo9.a in-guest.
//
// The prebuilt is read from <o9dir>/release/386, the language's release artifact
// (a 386 Plan 9 build, consistent o9c+libo9.a pair). Staging it there keeps the
// prebuilt in the language repo (the thing being distributed); halatha's job is
// only to install it. To refresh the prebuilt, rebuild on a 9legacy/9front guest
// (`mk install` to a prefix) and drop the result into release/386. Modes are
// passed explicitly (0775 for executables, 0664 for data) so the install is
// independent of the host file's exec bit. Idempotent: addFile upserts by path.
//
// The 9legacy branch carries the adaptation for 9legacy's lib9p, which does not
// expose 9front's srvrelease/srvacquire: the facade codegen in
// o9c/grammar.d/50-app-facade.y emits compat no-op shims and #defines them over
// the real symbols. The release/386 prebuilt reflects that, so the bundled
// toolchain targets 9legacy, not just 9front.
func installO9c(entries *[]archiveEntry, o9dir string) error {
	now := uint32(time.Now().Unix())
	pre := filepath.Join(o9dir, "release", "386")
	if _, err := os.Stat(pre); err != nil {
		return fmt.Errorf("install o9c: prebuilt release not found at %s: %w", pre, err)
	}

	// Required: the compiler, the runtime archive, the header. A missing one is
	// fatal -- the toolchain is unusable without it. o9c is an exec binary
	// (0775); libo9.a and o9.h are data (0664).
	requires := []struct{ src, dst string; mode uint32 }{
		{filepath.Join(pre, "bin", "o9c"), "/386/bin/o9c", 0775},
		{filepath.Join(pre, "lib", "libo9.a"), "/386/lib/libo9.a", 0664},
		{filepath.Join(pre, "include", "o9.h"), "/sys/include/o9.h", 0664},
	}
	for _, f := range requires {
		if err := addFile(entries, f.src, f.dst, f.mode, true, now); err != nil {
			return fmt.Errorf("install o9c %s: %w", f.dst, err)
		}
	}

	// rc build helpers -> /rc/bin (o9build orchestrates transpile+compile+link;
	// o9proj scaffolds a project; o9plumb/o9plumbbuild wire plumbing). Exec.
	for _, name := range []string{"o9build", "o9proj", "o9plumb", "o9plumbbuild"} {
		p := filepath.Join(pre, "rc", name)
		if _, err := os.Stat(p); err != nil {
			fmt.Fprintf(os.Stderr, "mk9legacyimg: o9c helper %s missing; skipping\n", name)
			continue
		}
		if err := addFile(entries, p, "/rc/bin/"+name, 0775, true, now); err != nil {
			return fmt.Errorf("install %s: %w", name, err)
		}
	}

	// Runtime sources -> /sys/src/cmd, so `mk` can rebuild libo9.a in-guest if
	// needed. Data. Non-fatal if absent (the prebuilt archive is installed).
	for _, name := range []string{"o9_dispatch.s", "o9_runtime.c", "o9_tab_discard.c"} {
		p := filepath.Join(pre, "src", name)
		if _, err := os.Stat(p); err != nil {
			continue
		}
		if err := addFile(entries, p, "/sys/src/cmd/"+name, 0664, true, now); err != nil {
			return fmt.Errorf("install %s: %w", name, err)
		}
	}

	// Stdlib modules -> /sys/lib/o9/stdlib. The staged release/386/stdlib is the
	// canonical install set (the 14 core modules + auth_facade_app); read the
	// dir and install every .o9 so a refreshed release need not touch this code.
	stdlibDir := filepath.Join(pre, "stdlib")
	dirEntries, err := os.ReadDir(stdlibDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mk9legacyimg: o9c stdlib dir %s unreadable: %v\n", stdlibDir, err)
		return nil
	}
	for _, e := range dirEntries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".o9") {
			continue
		}
		p := filepath.Join(stdlibDir, e.Name())
		if err := addFile(entries, p, "/sys/lib/o9/stdlib/"+e.Name(), 0664, true, now); err != nil {
			return fmt.Errorf("install stdlib %s: %w", e.Name(), err)
		}
	}
	return nil
}

func addPayloads(entries *[]archiveEntry, adds, addTrees []string) error {
	now := uint32(time.Now().Unix())
	for _, spec := range adds {
		src, dst, mode, modeSet, err := parseAddSpec(spec)
		if err != nil {
			return err
		}
		if err := addFile(entries, src, dst, mode, modeSet, now); err != nil {
			return err
		}
	}
	for _, spec := range addTrees {
		src, dst, err := parseAddTreeSpec(spec)
		if err != nil {
			return err
		}
		if err := addTree(entries, src, dst, now); err != nil {
			return err
		}
	}
	return nil
}

func parseAddSpec(spec string) (src, dst string, mode uint32, modeSet bool, err error) {
	parts := strings.Split(spec, ":")
	if len(parts) != 2 && len(parts) != 3 {
		return "", "", 0, false, fmt.Errorf("bad -add %q: want src:dst[:octal-mode]", spec)
	}
	src = parts[0]
	dst, err = cleanArchivePath(parts[1])
	if err != nil {
		return "", "", 0, false, fmt.Errorf("bad -add %q: %w", spec, err)
	}
	if len(parts) == 3 {
		v, err := strconv.ParseUint(parts[2], 8, 32)
		if err != nil {
			return "", "", 0, false, fmt.Errorf("bad -add %q mode: %w", spec, err)
		}
		mode = uint32(v)
		modeSet = true
	}
	return src, dst, mode, modeSet, nil
}

func parseAddTreeSpec(spec string) (src, dst string, err error) {
	parts := strings.Split(spec, ":")
	if len(parts) != 2 {
		return "", "", fmt.Errorf("bad -addtree %q: want srcdir:dstdir", spec)
	}
	dst, err = cleanArchivePath(parts[1])
	if err != nil {
		return "", "", fmt.Errorf("bad -addtree %q: %w", spec, err)
	}
	return parts[0], dst, nil
}

func addFile(entries *[]archiveEntry, src, dst string, mode uint32, modeSet bool, now uint32) error {
	info, err := os.Stat(src)
	if err != nil {
		return fmt.Errorf("stat %s: %w", src, err)
	}
	if info.IsDir() {
		return fmt.Errorf("%s is a directory; use -addtree", src)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", src)
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("read %s: %w", src, err)
	}
	if !modeSet {
		mode = 0664
		if info.Mode()&0111 != 0 {
			mode = 0775
		}
	}
	if err := ensureArchiveDirs(entries, path.Dir(dst), now); err != nil {
		return err
	}
	upsert(entries, archiveEntry{
		Path:  dst,
		Mode:  mode,
		UID:   "sys",
		GID:   "sys",
		Mtime: now,
		Data:  data,
	})
	return nil
}

func addTree(entries *[]archiveEntry, srcRoot, dstRoot string, now uint32) error {
	rootInfo, err := os.Stat(srcRoot)
	if err != nil {
		return fmt.Errorf("stat %s: %w", srcRoot, err)
	}
	if !rootInfo.IsDir() {
		return fmt.Errorf("%s is not a directory", srcRoot)
	}
	if err := ensureArchiveDirs(entries, dstRoot, now); err != nil {
		return err
	}
	return filepath.WalkDir(srcRoot, func(src string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(srcRoot, src)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		dst, err := cleanArchivePath(path.Join(dstRoot, filepath.ToSlash(rel)))
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("%s is a symlink; refusing to archive it", src)
		}
		if d.IsDir() {
			return ensureArchiveDirs(entries, dst, now)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		return addFile(entries, src, dst, 0, false, now)
	})
}

func cleanArchivePath(p string) (string, error) {
	if strings.TrimSpace(p) == "" {
		return "", errors.New("empty archive path")
	}
	p = strings.ReplaceAll(p, "\\", "/")
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	p = path.Clean(p)
	if p == "/" {
		return "", errors.New("archive path cannot be /")
	}
	return p, nil
}

func ensureArchiveDirs(entries *[]archiveEntry, dir string, now uint32) error {
	dir, err := cleanArchivePath(dir)
	if err != nil {
		if strings.Contains(err.Error(), "archive path cannot be /") {
			return nil
		}
		return err
	}
	cur := ""
	for _, part := range strings.Split(strings.TrimPrefix(dir, "/"), "/") {
		if part == "" {
			continue
		}
		cur += "/" + part
		ent := findEntry(*entries, cur)
		if ent != nil {
			if ent.Mode&dmDir == 0 {
				return fmt.Errorf("%s exists in archive and is not a directory", cur)
			}
			continue
		}
		upsert(entries, archiveEntry{
			Path:  cur,
			Mode:  dmDir | 0775,
			UID:   "sys",
			GID:   "sys",
			Mtime: now,
		})
	}
	return nil
}

const wanixNetScript = `#!/bin/rc
# Bring up browser-backed v86 networking without blocking rio startup.
# v86's ne2k NIC + fetch/DHCP backend hands the guest 192.168.86.100
# (router 192.168.86.1). At boot the guest is busy (rio, usbd) and the
# first DHCP exchanges can drop packets to v86's single-threaded render
# loop, so we wait for the NIC and keep retrying ipconfig well past the
# busy boot window instead of giving up after a few tries. (rc has no
# break/continue builtins, so the loops use guard conditions and run
# their full iteration count, acting only while the condition is unmet.)
TIMESYNCARGS=(-ra1000000)

# wait for the ne2k (v86 NIC) to attach; can take a few seconds at boot
for(i in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20 21 22 23 24 25 26 27 28 29 30){
	if(! test -e /net/ether0)
		sleep 1
}
if(! test -e /net/ether0)
	exit ''

# keep retrying until v86's DHCP gives us 192.168.86.100. ip/ipconfig
# daemonizes a lease watcher and returns in <=4s per call, so this stays
# bounded and non-blocking. Suppress stderr: v86's OFFER omits DHCP
# option 51 (lease time), so ipconfig prints a benign "Offer with 0
# lease, using 900" warning that would otherwise clutter the boot log.
for(i in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15){
	if(! grep -s '192.168.86.100' /net/ipselftab)
		ip/ipconfig >[2]/dev/null
	if(! grep -s '192.168.86.100' /net/ipselftab)
		sleep 3
}

if(! grep -s '192.168.86.100' /net/ipselftab)
	exit 'ipconfig failed'

if(! test -e /srv/dns)
	ndb/dns -r

if(! ps|grep -s timesync)
	if(! ~ $TIMESYNCARGS '')
		aux/timesync $TIMESYNCARGS
`

func patchTermRCContents(data []byte) []byte {
	data = bytes.Replace(data,
		[]byte("if(! test -e /net/ipifc/0/ctl)\n\tip/ipconfig\n"),
		[]byte("# wanix: browser-backed networking starts in the background when a NIC is present.\nif(test -x /rc/bin/wanixnet)\n\t/rc/bin/wanixnet &\n"),
		1,
	)
	data = bytes.Replace(data,
		[]byte("\t# start timesync if it isn't running and we weren't told not to\n\tif(! ps|grep -s timesync)\n\t\tif(! ~ $TIMESYNCARGS '')\n\t\t\taux/timesync $TIMESYNCARGS\n"),
		[]byte("\t# wanix: /rc/bin/wanixnet starts timesync after DHCP succeeds.\n"),
		1,
	)
	data = bytes.Replace(data,
		[]byte("\nusbstart\n"),
		[]byte("\n# wanix: v86 boots do not need USB startup unless explicitly requested.\nif(~ $wanixusb 1)\n\tusbstart\n"),
		1,
	)
	return data
}

// stripSerial9pBlock removes the legacy serial9p /n/host bridge block that an
// earlier -serial9p build appended to termrc, so a -virtio9p build does not
// double-mount /n/host (serial9p over /srv/host AND #9/0). The block runs from
// the "# wanix /n/host bridge over eia1" marker through the closing "}" of the
// `if(test -x /bin/serial9p){` block, plus the trailing blank line. Leaves the
// data unchanged if the marker or the closing brace is not found.
func stripSerial9pBlock(data []byte) []byte {
	const marker = "# wanix /n/host bridge over eia1"
	lines := bytes.Split(data, []byte("\n"))
	startIdx := -1
	for i, ln := range lines {
		if bytes.HasPrefix(ln, []byte(marker)) {
			startIdx = i
			break
		}
	}
	if startIdx < 0 {
		return data
	}
	endIdx := -1
	for i := startIdx + 1; i < len(lines); i++ {
		if bytes.Equal(lines[i], []byte("}")) {
			endIdx = i
			break
		}
	}
	if endIdx < 0 {
		return data
	}
	keep := append([][]byte{}, lines[:startIdx]...)
	rest := lines[endIdx+1:]
	if len(rest) > 0 && len(rest[0]) == 0 {
		rest = rest[1:] // drop the blank line that followed the block
	}
	keep = append(keep, rest...)
	return bytes.Join(keep, []byte("\n"))
}

func findEntry(entries []archiveEntry, path string) *archiveEntry {
	for i := range entries {
		if entries[i].Path == path {
			return &entries[i]
		}
	}
	return nil
}

func upsert(entries *[]archiveEntry, ent archiveEntry) {
	for i := range *entries {
		if (*entries)[i].Path == ent.Path {
			(*entries)[i] = ent
			return
		}
	}
	*entries = append(*entries, ent)
}

func failf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "mk9legacyimg: "+format+"\n", args...)
	os.Exit(1)
}
