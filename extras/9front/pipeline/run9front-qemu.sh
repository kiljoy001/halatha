#!/bin/sh
set -eu

disk=${1:-local/9front-halatha.img}
iso=${ISO:-local/9front-11957.386.iso}
payload=${PAYLOAD:-local/halatha-9front-payload.iso}
mem=${MEM:-1024}
boot=${BOOT:-d}
installer=${INSTALLER:-1}
payload_index=${PAYLOAD_INDEX:-1}

if ! command -v qemu-system-i386 >/dev/null 2>&1; then
	echo "run9front-qemu: qemu-system-i386 not found" >&2
	exit 1
fi

if ! test -e "$disk"; then
	echo "run9front-qemu: disk not found: $disk" >&2
	exit 1
fi
if test "$installer" != 0 && ! test -e "$iso"; then
	echo "run9front-qemu: installer ISO not found: $iso" >&2
	exit 1
fi

iso_args=
if test "$installer" != 0; then
	iso_args="-drive file=$iso,format=raw,media=cdrom,index=2,readonly=on"
fi

payload_args=
if test -e "$payload"; then
	payload_args="-drive file=$payload,format=raw,media=cdrom,if=ide,index=$payload_index,readonly=on"
fi

exec qemu-system-i386 \
	-m "$mem" \
	-rtc base=localtime \
	-drive file="$disk",format=raw,if=ide,index=0 \
	$iso_args \
	$payload_args \
	-netdev user,id=n0 \
	-device ne2k_pci,netdev=n0 \
	-boot "$boot"
