/* htest: hash files repeatedly; every read must produce the same hash.
 * Catches torn/duplicated/zeroed chunks in devmnt/9P reads. */
#include <u.h>
#include <libc.h>

static u64
hashfile(char *path, long *lenp)
{
	char buf[8192];
	u64 h = 1469598103934665603ULL;
	long tot = 0;
	int fd, n, i;

	fd = open(path, OREAD);
	if(fd < 0){
		print("HTEST open fail %s\n", path);
		exits("open");
	}
	while((n = read(fd, buf, sizeof buf)) > 0){
		tot += n;
		for(i = 0; i < n; i++){
			h ^= (u8)buf[i];
			h *= 1099511628211ULL;
		}
	}
	close(fd);
	*lenp = tot;
	return h;
}

void
main(int argc, char **argv)
{
	int rounds = 200, i;
	long len;
	u64 href = 0;

	if(argc > 1 && strcmp(argv[1], "kid") == 0){
		u64 h = hashfile("/bin/rc", &len);
		u64 h2 = hashfile("/rc/lib/rcmain", &len);
		print("HKID %llx %llx\n", h, h2);
		exits(nil);
	}
	for(i = 0; i < rounds; i++){
		long l2;
		u64 h = hashfile("/bin/rc", &l2);
		if(i == 0){
			href = h;
			len = l2;
			print("HTEST ref hash %llx len %ld\n", h, len);
		} else if(h != href || l2 != len){
			print("HTEST-MISMATCH round %d hash %llx len %ld want %llx %ld\n",
				i, h, l2, href, len);
			exits("mismatch");
		}
	}
	print("HTEST-DONE rounds %d ok\n", rounds);
	exits(nil);
}
