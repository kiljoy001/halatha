/* ztest: scan virgin sbrk pages for nonzero bytes, under fork/exec churn.
 * Fresh break growth must be zero-filled by the kernel. Any nonzero
 * byte means a stale/wrong page was mapped. */
#include <u.h>
#include <libc.h>

static int
scan(char *p, usize n, int round, int kid)
{
	usize i;
	int bad = 0;
	for(i = 0; i < n; i++){
		if(p[i] != 0){
			if(bad < 4)
				print("ZTEST-NONZERO round %d kid %d off %ld val 0x%02x addr %p\n",
					round, kid, (long)i, p[i] & 0xff, p + i);
			bad++;
		}
	}
	return bad;
}

void
main(int argc, char **argv)
{
	int rounds = 200, i, bad = 0;
	usize sz = 256 * 1024;
	char *p;
	Waitmsg *w;

	if(argc > 1)
		rounds = atoi(argv[1]);
	for(i = 0; i < rounds; i++){
		int pid = fork();
		if(pid == 0){
			/* child: grow break by sz, scan, then exec echo or exit */
			p = sbrk(sz);
			if(p == (void*)-1)
				exits("sbrk");
			if(scan(p, sz, i, 1))
				exits("nonzero");
			exits(nil);
		}
		if(pid < 0){
			print("ZTEST fork fail\n");
			exits("fork");
		}
		w = wait();
		if(w == nil || w->msg[0] != '\0'){
			print("ZTEST child bad round %d msg '%s'\n", i, w ? w->msg : "?");
			bad++;
		}
		free(w);
		/* parent also grows its own break periodically */
		if(i % 16 == 0){
			p = sbrk(sz);
			if(p == (void*)-1)
				exits("sbrk");
			if(scan(p, sz, i, 0))
				bad++;
		}
	}
	print("ZTEST-DONE rounds %d bad %d\n", rounds, bad);
	exits(nil);
}
