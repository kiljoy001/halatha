#include <u.h>
#include <libc.h>

static char sbuf[65536];
static char lbuf[65536];
static int nlast = -1;

static int
readfile(char *path, char *buf, int max)
{
	int fd, n, tot = 0;

	fd = open(path, OREAD);
	if(fd < 0)
		return -1;
	while(tot < max && (n = read(fd, buf + tot, max - tot)) > 0)
		tot += n;
	close(fd);
	return tot;
}

static void
writefile(char *path, char *buf, int n)
{
	int fd;

	fd = open(path, OWRITE);
	if(fd < 0)
		return;
	write(fd, buf, n);
	close(fd);
}

void
main(void)
{
	int n;

	writefile("/dev/snarf", "rio\n", 4);
	for(;;){
		if(access("/n/host/clip/inbox.new", 0) >= 0){
			n = readfile("/n/host/clip/inbox.new", sbuf, sizeof sbuf);
			if(n >= 0)
				writefile("/dev/snarf", sbuf, n);
			remove("/n/host/clip/inbox.new");
		}
		n = readfile("/dev/snarf", sbuf, sizeof sbuf);
		if(n >= 0 && (n != nlast || memcmp(sbuf, lbuf, n) != 0)){
			writefile("/n/host/clip/text", sbuf, n);
			memcpy(lbuf, sbuf, n);
			nlast = n;
		}
		sleep(2000);
	}
}
