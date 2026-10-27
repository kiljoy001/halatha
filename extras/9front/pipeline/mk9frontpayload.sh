#!/bin/sh
set -eu

out=${1:-local/halatha-9front-payload.iso}
o9dir=${O9DIR:-../objective-9c}
ygg_bin=${YGG_BIN:-local/yggdrasil-plan9-386/yggdrasil}
yggctl_bin=${YGGCTL_BIN:-local/yggdrasil-plan9-386/yggdrasilctl}
halctl_bin=${HALCTL_BIN:-local/halctl.386}
work=${WORKDIR:-local/halatha-9front-payload}

if ! command -v genisoimage >/dev/null 2>&1; then
	echo "mk9frontpayload: genisoimage not found" >&2
	exit 1
fi

release=$o9dir/release/386
if ! test -d "$release"; then
	echo "mk9frontpayload: o9 release not found at $release" >&2
	exit 1
fi
if ! test -f "$halctl_bin"; then
	echo "mk9frontpayload: halctl missing at $halctl_bin (set HALCTL_BIN)" >&2
	exit 1
fi

rm -rf "$work"
mkdir -p \
	"$work/386/bin" \
	"$work/386/lib" \
	"$work/sys/include" \
	"$work/rc/bin" \
	"$work/sys/src/cmd" \
	"$work/sys/lib/o9/stdlib" \
	"$work/lib" \
	"$work/install"

cp "$release/bin/o9c" "$work/386/bin/o9c"
cp "$halctl_bin" "$work/386/bin/halctl"
cp "$release/lib/libo9.a" "$work/386/lib/libo9.a"
cp "$release/include/o9.h" "$work/sys/include/o9.h"
cp "$release"/rc/o9* "$work/rc/bin/"
cp "$release"/src/* "$work/sys/src/cmd/"
cp "$release"/stdlib/*.o9 "$work/sys/lib/o9/stdlib/"

cp extras/9front/halatha.local "$work/rc/bin/halatha.local"
cp extras/9front/ygg-up.rc "$work/rc/bin/ygg-up"
cp extras/9front/ygg-peers.rc "$work/rc/bin/ygg-peers"
cp extras/9front/yggdrasil.peers "$work/lib/yggdrasil.peers"
cp extras/9front/termrc.local "$work/rc/bin/termrc.local.halatha"
cp extras/9front/pipeline/halatha-grid-terminal.rc "$work/install/halatha-grid-terminal.rc"
cp extras/9front/pipeline/install-halatha-http.rc "$work/install/install-halatha-http.rc"

if test -n "$ygg_bin" && test -e "$ygg_bin"; then
	cp "$ygg_bin" "$work/386/bin/yggdrasil"
fi
if test -n "$yggctl_bin" && test -e "$yggctl_bin"; then
	cp "$yggctl_bin" "$work/386/bin/yggdrasilctl"
fi

cat > "$work/install/install-halatha.rc" <<'EOF'
#!/bin/rc
rfork e

src=/mnt/cd
if(! test -e $src/386/bin/o9c)
	src=/n/cd
if(! test -e $src/386/bin/o9c){
	echo 'halatha install: mount payload iso at /mnt/cd or /n/cd first'
	exit 'no payload'
}

fn cpfile {
	from=$1
	to=$2
	if(test -e $to)
		rm -f $to
	cp $from $to
}

for(d in /386/bin /386/lib /sys/include /rc/bin /lib /sys/src/cmd /sys/lib/o9 /sys/lib/o9/stdlib){
	if(! test -d $d)
		mkdir $d
}

for(f in $src/386/bin/*)
	cpfile $f /386/bin/`{basename $f}
for(f in $src/386/lib/*)
	cpfile $f /386/lib/`{basename $f}
for(f in $src/sys/include/*)
	cpfile $f /sys/include/`{basename $f}
for(f in $src/rc/bin/*)
	cpfile $f /rc/bin/`{basename $f}
for(f in $src/lib/*)
	cpfile $f /lib/`{basename $f}
for(f in $src/sys/src/cmd/*)
	cpfile $f /sys/src/cmd/`{basename $f}
for(f in $src/sys/lib/o9/stdlib/*)
	cpfile $f /sys/lib/o9/stdlib/`{basename $f}

chmod +x /386/bin/o9c /386/bin/halctl /rc/bin/o9* /rc/bin/halatha.local /rc/bin/ygg-up /rc/bin/ygg-peers
if(test -e /386/bin/yggdrasil)
	chmod +x /386/bin/yggdrasil
if(test -e /386/bin/yggdrasilctl)
	chmod +x /386/bin/yggdrasilctl

if(! grep -s 'halatha.local' /rc/bin/termrc){
	echo '' >>/rc/bin/termrc
	echo '# halatha optional local boot hook' >>/rc/bin/termrc
	echo 'if(test -r /n/host/boot/halatha.local)' >>/rc/bin/termrc
	echo '	rc /n/host/boot/halatha.local &' >>/rc/bin/termrc
	echo 'if(! test -r /n/host/boot/halatha.local && test -x /rc/bin/halatha.local)' >>/rc/bin/termrc
	echo '	/rc/bin/halatha.local &' >>/rc/bin/termrc
}

echo 'halatha install: done'
EOF

cat > "$work/README" <<'EOF'
Halatha 9front payload

Mount this ISO inside the installed 9front system, then run:

  rc /mnt/cd/install/install-halatha.rc

or:

  rc /n/cd/install/install-halatha.rc

This installs halctl, o9c, o9 runtime files, Yggdrasil startup scripts, peers, and
optional yggdrasil/yggdrasilctl binaries when YGG_BIN/YGGCTL_BIN were supplied
while building the payload ISO.

For the terminal-only grid image shape, also run:

  rc /mnt/cd/install/halatha-grid-terminal.rc
EOF

mkdir -p "$(dirname "$out")"
genisoimage -quiet -R -J -V HALATHA9FRONT -o "$out" "$work"
bytes=$(stat -c %s "$out")
echo "$out $bytes bytes"
