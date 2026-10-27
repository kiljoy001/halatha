#include <u.h>
#include <libc.h>

/* Fixed test traffic secrets.  Never use these outside an isolated VM test. */
static char *keys = "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8gISIjJCUmJygpKissLS4vMDEyMzQ1Njc=";
static char *badkeys = "AQECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8gISIjJCUmJygpKissLS4vMDEyMzQ1Njc=";
static char *keys256 = "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8gISIjJCUmJygpKissLS4vMDEyMzQ1Njc4OTo7PD0+P0BBQkNERUZHSElKS0xNTk9QUVJTVFVWVw==";
static uchar buf[32768];

static void
ctlwrite(int fd, char *s)
{
	if(write(fd, s, strlen(s)) < 0)
		sysfatal("tls ctl %s: %r", s);
}

static int
wrap(int fd, int isclient, int bad, int bits)
{
	char id[16], path[64], cmd[160];
	int ctl, data, n;

	ctl = open("/net/tls/clone", ORDWR);
	if(ctl < 0)
		sysfatal("open tls clone: %r");
	n = read(ctl, id, sizeof(id)-1);
	if(n < 1)
		sysfatal("read tls clone: %r");
	id[n] = 0;
	snprint(cmd, sizeof(cmd), "fd %d 0x304", fd);
	ctlwrite(ctl, cmd);
	ctlwrite(ctl, "version 0x304");
	snprint(cmd, sizeof(cmd), "secret13 aes_%d_gcm %d %s", bits, isclient,
		bits == 256 ? keys256 : bad ? badkeys : keys);
	ctlwrite(ctl, cmd);
	ctlwrite(ctl, "cipher13 both");
	ctlwrite(ctl, "opened");
	snprint(path, sizeof(path), "/net/tls/%s/data", id);
	data = open(path, ORDWR);
	if(data < 0)
		sysfatal("open tls data: %r");
	close(ctl);
	close(fd);
	return data;
}

static void
server(int bad)
{
	char adir[40], ldir[40];
	int afd, lfd, fd, n, i;

	afd = announce("tcp!*!16443", adir);
	if(afd < 0)
		sysfatal("announce: %r");
	print("TLS13 SERVER READY\n");
	lfd = listen(adir, ldir);
	if(lfd < 0)
		sysfatal("listen: %r");
	fd = accept(lfd, ldir);
	if(fd < 0)
		sysfatal("accept: %r");
	close(lfd);
	close(afd);
	fd = wrap(fd, 0, bad, 128);
	n = readn(fd, buf, 5);
	if(bad){
		if(n >= 0)
			sysfatal("bad traffic key accepted: %d", n);
		print("TLS13 BAD TAG REJECTED\n");
		close(fd);
		return;
	}
	if(n != 5 || memcmp(buf, "hello", 5) != 0)
		sysfatal("server read hello: %d %r", n);
	if(write(fd, "world", 5) != 5)
		sysfatal("server write world: %r");
	if(readn(fd, buf, sizeof(buf)) != sizeof(buf))
		sysfatal("server read bulk: %r");
	for(i=0; i<sizeof(buf); i++)
		if(buf[i] != (uchar)i)
			sysfatal("server bulk mismatch at %d", i);
	if(write(fd, buf, sizeof(buf)) != sizeof(buf))
		sysfatal("server write bulk: %r");
	print("TLS13 KERNEL SERVER PASS\n");
	close(fd);
}

static void
client(int bad, int bits, char *target)
{
	int fd, i;
	char dir[40];

	fd = dial(target, nil, dir, nil);
	if(fd < 0)
		sysfatal("dial: %r");
	fd = wrap(fd, 1, 0, bits);
	if(write(fd, "hello", 5) != 5)
		sysfatal("client write hello: %r");
	if(bad){
		close(fd);
		print("TLS13 BAD TAG SENT\n");
		return;
	}
	if(readn(fd, buf, 5) != 5 || memcmp(buf, "world", 5) != 0)
		sysfatal("client read world: %r");
	for(i=0; i<sizeof(buf); i++)
		buf[i] = i;
	if(write(fd, buf, sizeof(buf)) != sizeof(buf))
		sysfatal("client write bulk: %r");
	if(readn(fd, buf, sizeof(buf)) != sizeof(buf))
		sysfatal("client read bulk: %r");
	for(i=0; i<sizeof(buf); i++)
		if(buf[i] != (uchar)i)
			sysfatal("client bulk mismatch at %d", i);
	print("TLS13 KERNEL CLIENT PASS\n");
	close(fd);
}

static void
handhost(void)
{
	char id[16], path[64], cmd[160], dir[40];
	int fd, ctl, hand, data, n;

	fd = dial("tcp!10.0.2.2!16443", nil, dir, nil);
	if(fd < 0)
		sysfatal("dial: %r");
	ctl = open("/net/tls/clone", ORDWR);
	if(ctl < 0)
		sysfatal("open tls clone: %r");
	n = read(ctl, id, sizeof(id)-1);
	if(n < 1)
		sysfatal("read tls clone: %r");
	id[n] = 0;
	snprint(cmd, sizeof(cmd), "fd %d 0x304", fd);
	ctlwrite(ctl, cmd);
	ctlwrite(ctl, "version 0x304");
	snprint(cmd, sizeof(cmd), "secret13 aes_128_gcm 1 %s", keys);
	ctlwrite(ctl, cmd);
	ctlwrite(ctl, "cipher13 both");
	snprint(path, sizeof(path), "/net/tls/%s/hand", id);
	hand = open(path, ORDWR);
	if(hand < 0)
		sysfatal("open tls hand: %r");
	snprint(path, sizeof(path), "/net/tls/%s/data", id);
	data = open(path, ORDWR);
	if(data < 0)
		sysfatal("open tls data: %r");
	close(fd);
	if(readn(hand, buf, 5) != 5 || memcmp(buf, "ready", 5) != 0)
		sysfatal("encrypted handshake after CCS: %r");
	ctlwrite(ctl, "opened");
	if(readn(data, buf, 5) != 5 || memcmp(buf, "world", 5) != 0)
		sysfatal("application after CCS: %r");
	print("TLS13 KERNEL CCS PASS\n");
	close(data);
	close(hand);
	close(ctl);
}

static void
plainhost(void)
{
	char id[16], path[64], cmd[80], dir[40];
	int fd, ctl, hand, n;

	fd = dial("tcp!10.0.2.2!16443", nil, dir, nil);
	if(fd < 0)
		sysfatal("dial: %r");
	ctl = open("/net/tls/clone", ORDWR);
	if(ctl < 0)
		sysfatal("open tls clone: %r");
	n = read(ctl, id, sizeof(id)-1);
	if(n < 1)
		sysfatal("read tls clone: %r");
	id[n] = 0;
	snprint(cmd, sizeof(cmd), "fd %d 0x304", fd);
	ctlwrite(ctl, cmd);
	ctlwrite(ctl, "version 0x304");
	snprint(path, sizeof(path), "/net/tls/%s/hand", id);
	hand = open(path, ORDWR);
	if(hand < 0)
		sysfatal("open tls hand: %r");
	close(fd);
	if(readn(hand, buf, 5) != 5 || memcmp(buf, "hello", 5) != 0)
		sysfatal("plaintext initial ClientHello: %r");
	print("TLS13 INITIAL HELLO PASS\n");
	close(hand);
	close(ctl);
}

void
main(int argc, char **argv)
{
	if(argc < 2)
		sysfatal("usage: tls13test server|client|serverbad|clientbad");
	if(strcmp(argv[1], "server") == 0)
		server(0);
	else if(strcmp(argv[1], "client") == 0)
		client(0, 128, "tcp!10.44.0.1!16443");
	else if(strcmp(argv[1], "serverbad") == 0)
		server(1);
	else if(strcmp(argv[1], "clientbad") == 0)
		client(1, 128, "tcp!10.44.0.1!16443");
	else if(strcmp(argv[1], "clienthost") == 0)
		client(0, 128, "tcp!10.0.2.2!16443");
	else if(strcmp(argv[1], "clienthost256") == 0)
		client(0, 256, "tcp!10.0.2.2!16443");
	else if(strcmp(argv[1], "handhost") == 0)
		handhost();
	else if(strcmp(argv[1], "plainhost") == 0)
		plainhost();
	else
		sysfatal("unknown test mode");
	exits(nil);
}
