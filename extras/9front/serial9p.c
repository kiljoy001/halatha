/*
 * serial9p — expose a serial port as a mountable 9P service.
 *
 * Used by 9front under wanix/v86 to mount the host namespace. wanix serves the
 * namespace over 9P2000 on the guest's first serial port (v86 serial0). 9front's
 * console is on VGA, so serial0 is free for this bridge.
 *
 * serial9p opens the serial device read/write and posts it under /srv/<name>
 * (inlining lib9p's postfd, so it only needs libc). The kernel then mounts
 * /srv/<name> and speaks 9P directly over the serial fd. The process stays
 * alive holding the fd and the /srv entry open.
 *
 * Build on 9front (386): mk
 * Install: cp serial9p /bin/serial9p
 * Use (e.g. in /rc/bin/termrc.local):
 *	serial9p /dev/eia0 host &
 *	mount -c /srv/host /n/host
 */
#include <u.h>
#include <libc.h>

void
main(int argc, char **argv)
{
	char *dev, *name, buf[128];
	int sfd, cfd;

	dev = argc > 1 ? argv[1] : "/dev/eia0";
	name = argc > 2 ? argv[2] : "host";

	sfd = open(dev, ORDWR);
	if(sfd < 0)
		sysfatal("open %s: %r", dev);

	/* Inline postfd(name, sfd): create /srv/<name> (removed when we exit)
	 * and write the serial fd number into it. Mounters get a dup of sfd and
	 * speak 9P over it. ORCLOSE removes /srv/<name> when cfd closes. */
	snprint(buf, sizeof buf, "/srv/%s", name);
	cfd = create(buf, OWRITE|ORCLOSE|OCEXEC, 0600);
	if(cfd < 0)
		sysfatal("create %s: %r", buf);
	if(fprint(cfd, "%d", sfd) < 0)
		sysfatal("post %s: %r", name);

	/* Hold /srv/<name> (cfd) and the serial fd (sfd) open for the lifetime
	 * of the mount. The kernel does the actual 9P I/O on its dup of sfd. */
	for(;;)
		sleep(1000);
	/* not reached */
	exits(nil);
}