/* forktest: minimal fork/exec/wait reproducer, bypassing rc.
 * Fills malloc'd blocks with a pattern, then repeatedly forks and
 * execs /bin/echo. After every wait it re-verifies the pattern and
 * churns malloc/free. Any heap corruption shared across fork (bad
 * copy-on-write, pool damage) should surface as CORRUPT or a fault.
 */
#include <u.h>
#include <libc.h>

enum { Nblocks = 32, Blocksz = 256 };

static uchar *blocks[Nblocks];

static ulong
checksum(void)
{
	ulong h = 1469598103934665603UL;
	int i, j;
	for(i = 0; i < Nblocks; i++)
		for(j = 0; j < Blocksz; j++){
			h ^= blocks[i][j];
			h *= 1099511628211UL;
		}
	return h;
}

void
main(int argc, char **argv)
{
	int i, n, pid;
	ulong want;
	Waitmsg *w;
	char *args[52];

	n = 200;
	if(argc > 1)
		n = atoi(argv[1]);
	for(i = 0; i < Nblocks; i++){
		blocks[i] = malloc(Blocksz);
		if(blocks[i] == nil)
			exits("malloc");
		memset(blocks[i], i * 7 + 1, Blocksz);
	}
	want = checksum();
	{
		static char wbuf[50][8];
		int wi;
		args[0] = "echo";
		for(wi = 0; wi < 49; wi++){
			snprint(wbuf[wi], sizeof wbuf[wi], "w%d", wi + 1);
			args[wi + 1] = wbuf[wi];
		}
		args[50] = nil;
	}
	/* realloc growth stress, rc codebuf style: grow by steps,
	 * verify the preserved prefix every time */
	{
		int sz, k;
		uchar *rb = nil;
		for(sz = 100; sz <= 20000; sz += 100){
			rb = realloc(rb, sz);
			if(rb == nil){ print("FORKTEST realloc nil %d\n", sz); exits("realloc"); }
			for(k = 0; k < sz - 100; k++)
				if(rb[k] != (uchar)(k * 31 + 7)){
					print("FORKTEST-REALLOC-CORRUPT sz %d at %d\n", sz, k);
					exits("rcorrupt");
				}
			for(k = sz - 100; k < sz; k++)
				rb[k] = (uchar)(k * 31 + 7);
		}
		free(rb);
		print("forktest: realloc phase ok\n");
	}
	/* rc-like churn: many small blocks, scattered frees, and a
	 * codebuf-style array grown by realloc across them, all
	 * pattern-verified. This exercises pool free-tree merging
	 * far harder than the tidy phases above. */
	{
		enum { NB = 400 };
		static uchar *ptrs[NB];
		static int sizes[NB];
		ulong rng = 88172645463325252UL;
		int i2, k, round;
		uchar *cb = nil;
		int cbcap = 0;
		for(round = 0; round < 30; round++){
			for(i2 = 0; i2 < NB; i2++){
				if(ptrs[i2] == nil){
					rng ^= rng << 13; rng ^= rng >> 7; rng ^= rng << 17;
					sizes[i2] = 8 + (rng % 504);
					ptrs[i2] = malloc(sizes[i2]);
					if(ptrs[i2] == nil){ print("FORKTEST churn malloc nil\n"); exits("malloc"); }
					memset(ptrs[i2], (uchar)i2, sizes[i2]);
				}
			}
			/* grow the codebuf-like array in 100-unit steps */
			{
				int want = cbcap + 100, j;
				cb = realloc(cb, want * 16);
				if(cb == nil){ print("FORKTEST churn realloc nil\n"); exits("realloc"); }
				for(j = 0; j < cbcap * 16; j++)
					if(cb[j] != (uchar)(j % 251)){
						print("FORKTEST-CHURN-CORRUPT round %d at %d\n", round, j);
						exits("ccorrupt");
					}
				for(j = cbcap * 16; j < want * 16; j++)
					cb[j] = (uchar)(j % 251);
				cbcap = want;
			}
			/* free every third block */
			for(i2 = 0; i2 < NB; i2 += 3){
				if(ptrs[i2]){
					for(k = 0; k < sizes[i2]; k++)
						if(ptrs[i2][k] != (uchar)i2){
							print("FORKTEST-CHURN-BLOCK-CORRUPT round %d block %d\n", round, i2);
							exits("bcorrupt");
						}
					free(ptrs[i2]);
					ptrs[i2] = nil;
				}
			}
		}
		free(cb);
		print("forktest: churn phase ok\n");
	}
	/* simplemung pattern: a 64-byte node allocated just before a
	 * string buffer that grows by realloc in 100-byte steps to
	 * ~300 (Stralloc growth in rc's io flush). Verify the node
	 * (and a canary allocated after the string) are never touched. */
	{
		int rep, step, k;
		for(rep = 0; rep < 200; rep++){
			uchar *node = malloc(64);
			uchar *str = malloc(101);
			uchar *canary = malloc(64);
			if(!node || !str || !canary){ print("FORKTEST mung malloc nil\n"); exits("malloc"); }
			memset(node, 0xA1, 64);
			memset(canary, 0xC1, 64);
			for(step = 0; step < 3; step++){
				str = realloc(str, 101 + (step + 1) * 100);
				if(str == nil){ print("FORKTEST mung realloc nil\n"); exits("realloc"); }
				memset(str, 'x', 101 + (step + 1) * 100);
				for(k = 0; k < 64; k++){
					if(node[k] != 0xA1){
						print("FORKTEST-MUNG-NODE-CORRUPT rep %d step %d at %d: %ux\n", rep, step, k, node[k]);
						exits("mungnode");
					}
					if(canary[k] != 0xC1){
						print("FORKTEST-MUNG-CANARY-CORRUPT rep %d step %d at %d: %ux\n", rep, step, k, canary[k]);
						exits("mungcanary");
					}
				}
			}
			free(node); free(str); free(canary);
		}
		print("forktest: mung pattern ok\n");
	}
	for(i = 0; i < n; i++){
		pid = rfork(RFPROC|RFFDG|RFENVG|RFNOTEG);
		if(pid == 0){
			/* rc's child does heap work between fork and exec.
			 * Force pool growth (sbrk inside the pool lock) as the
			 * very first action: _tos->prof.pid is still the
			 * parent's pid here, and the sbrk return path rewrites
			 * it to ours. If pool owner tracking trips on that,
			 * this aborts. */
			{ void *p = malloc(4*1024*1024); if(p) memset(p, 0xC3, 4096); free(p); }
			exec("/bin/echo", args);
			exits("execfail");
		}
		if(pid < 0){
			print("FORKTEST rfork fail %d\n", i);
			exits("rfork");
		}
		w = wait();
		if(w == nil){
			print("FORKTEST wait fail %d\n", i);
			exits("wait");
		}
		if(w->msg[0]){
			print("FORKTEST-CHILDFAULT %d: %s\n", i, w->msg);
			exits("childfault");
		}
		free(w);
		/* churn the pool like a parser would */
		{ void *p = malloc(997); if(p) memset(p, 0xA5, 997); free(p); }
		if(checksum() != want){
			print("FORKTEST-CORRUPT at %d\n", i);
			exits("corrupt");
		}
		if(i % 50 == 49)
			print("forktest: %d ok\n", i + 1);
	}
	print("FORKTEST-OK %d\n", n);
	exits(nil);
}
