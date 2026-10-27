/* cowtest: fork copy-on-write integrity check, bypassing rc.
 * Parent fills a global, a global buffer, and a heap block with
 * patterns, then forks. The child overwrites the same virtual
 * addresses (global + heap + a fresh allocation) and exits through
 * a pipe. The parent verifies its copies after wait. Any write
 * leaking across the fork boundary prints COWTEST BAD.
 */
#include <u.h>
#include <libc.h>
#include <pool.h>

static int touchflag;
static int firstmalloc;
static int nochurn;
static int churnonly;
static int waitonly;
static int malloconly;
static int freeonly;
static char *preblocks[64];
static u32int gmagic = 0x600DF00D;
static char gbuf[4096];
static char *hbuf;

typedef struct MBhdr MBhdr;
typedef struct MFree MFree;
typedef struct MArena MArena;
struct MBhdr { u32int magic; u32int size; };
struct MFree { MBhdr h; MFree *left, *right, *next, *prev; };
struct MArena { MBhdr h; MArena *aup, *down; u32int asize; u32int pad; };

static void
dumpfree(MFree *t, int depth)
{
	if(t == nil || depth > 8)
		return;
	print("  TREE %p magic=%x size=%ud L=%p R=%p N=%p P=%p\n",
		t, t->h.magic, t->h.size, t->left, t->right, t->next, t->prev);
	dumpfree(t->left, depth+1);
	dumpfree(t->right, depth+1);
}

static void
dumppool(char *tag)
{
	MArena *a;
	MBhdr *b;
	print("POOL %s freeroot=%p arenalist=%p curfree=%ud cursize=%ud curalloc=%ud quantum=%ud minblock=%ud minarena=%ud maxsize=%ud flags=%ux private=%p alloc=%p\n",
		tag, mainmem->freeroot, mainmem->arenalist, mainmem->curfree,
		mainmem->cursize, mainmem->curalloc, mainmem->quantum,
		mainmem->minblock, mainmem->minarena, mainmem->maxsize,
		mainmem->flags, mainmem->private, mainmem->alloc);
	for(a = mainmem->arenalist; a; a = a->down){
		char *end = (char*)a + a->asize - sizeof(MBhdr);
		print(" ARENA %p magic=%x asize=%ud hdrsize=%ud\n", a, a->h.magic, a->asize, a->h.size);
		b = (MBhdr*)((char*)a + a->h.size);
		while((char*)b < end){
			print("  BLK %p magic=%x size=%ud\n", b, b->magic, b->size);
			if(b->size == 0 || b->size > (1<<24)) break;
			b = (MBhdr*)((char*)b + b->size);
		}
	}
	dumpfree(mainmem->freeroot, 0);
}

static int
check(const char *where, int iter)
{
	int i;
	if(gmagic != 0x600DF00D){
		print("COWTEST BAD %s iter=%d gmagic=%#ux\n", where, iter, gmagic);
		return 1;
	}
	for(i = 0; i < (int)sizeof gbuf; i++)
		if(gbuf[i] != 'A'){
			print("COWTEST BAD %s iter=%d gbuf[%d]=%c\n", where, iter, i, gbuf[i]);
			return 1;
		}
	for(i = 0; i < 4096; i++)
		if(hbuf[i] != 'H'){
			print("COWTEST BAD %s iter=%d hbuf[%d]=%c\n", where, iter, i, hbuf[i]);
			return 1;
		}
	return 0;
}

void
main(int argc, char **argv)
{
	int i, n, pid, bad, pfd[2];
	char c;
	Waitmsg *w;

	n = 300;
	if(argc > 1)
		n = atoi(argv[1]);
	if(argc > 2 && argv[2][0] == 't')
		touchflag = 1;
	if(argc > 2 && argv[2][0] == 'f')
		firstmalloc = 1;
	if(argc > 2 && argv[2][0] == 'n')
		nochurn = 1;
	if(argc > 2 && argv[2][0] == 'c')
		churnonly = 1;
	if(argc > 2 && argv[2][0] == 'w')
		waitonly = 1;
	if(argc > 2 && argv[2][0] == 'a'){
		malloconly = 1;
		nochurn = 0;
	}
	if(argc > 2 && argv[2][0] == 'e')
		freeonly = 1;
	if(malloconly || freeonly)
		nochurn = 1;	/* use await() for wait, no wait-malloc */
	memset(gbuf, 'A', sizeof gbuf);
	hbuf = malloc(4096);
	if(hbuf == nil){
		print("COWTEST malloc failed\n");
		exits("malloc");
	}
	memset(hbuf, 'H', 4096);
	print("COWTEST parent pid=%d\n", getpid());
	if(freeonly){
		int k;
		for(k = 0; k < 64; k++)
			preblocks[k] = malloc(200);
	}
	dumppool("parent-pre-fork");
	bad = 0;
	for(i = 0; i < n && !bad; i++){
		if(pipe(pfd) < 0){
			print("COWTEST pipe failed\n");
			exits("pipe");
		}
		pid = fork();
		if(pid == 0){
			char *t;
			char code;
			if(firstmalloc){
				mainmem->flags |= POOL_PARANOIA;
				t = malloc(200);
				write(pfd[1], "M", 1);
				exits("firstmalloc");
			}
			dumppool("child-post-fork");
			if(touchflag && mainmem->freeroot != nil){
				volatile char *vp = (char*)mainmem->freeroot;
				u32int sz = ((MFree*)vp)->h.size;
				u32int k;
				for(k = 0; k < sz; k += 512)
					vp[k] = vp[k];
			}
			code = check("in-child-premalloc", i) ? 'B' : 'G';
			write(pfd[1], &code, 1);
			getpid();
			write(pfd[1], "1", 1);
			t = malloc(200);
			write(pfd[1], "2", 1);
			gmagic = 0x0BAD0BAD;
			memset(gbuf, 'B', sizeof gbuf);
			memset(hbuf, 'B', 4096);
			if(t) memset(t, 'C', 200);
			write(pfd[1], "x", 1);
			exits("postmalloc");
		}
		close(pfd[1]);
		{
			char buf[16];
			int nb = 0, r;
			while((r = read(pfd[0], buf+nb, 1)) == 1 && nb < 15)
				nb++;
			buf[nb] = 0;
			if(nb == 0)
				print("COWTEST short pipe read iter=%d\n", i);
			print("COWTEST iter=%d pipebytes=[%s]\n", i, buf);
		}
		close(pfd[0]);
		bad |= check("after-read", i);
		if(nochurn || churnonly){
			char wbuf[256];
			int nw = await(wbuf, sizeof wbuf - 1);
			if(nw > 0){ wbuf[nw] = 0; print("COWTEST iter=%d await=[%s]\n", i, wbuf); }
		}else{
			w = wait();
			if(w){ print("COWTEST iter=%d child %d status=[%s]\n", i, w->pid, w->msg); free(w); }
		}
		bad |= check("after-wait", i);
		/* churn: allocate/free like a thread struct lifecycle */
		if(malloconly){
			char *t = malloc(200);
			if(t) memset(t, 'P', 200);
		}else if(freeonly){
			if(preblocks[i]){ memset(preblocks[i], 'P', 200); free(preblocks[i]); }
		}else if(!nochurn && !waitonly){
			char *t = malloc(200);
			if(t) memset(t, 'P', 200);
			free(t);
		}
		bad |= check("after-churn", i);
	}
	if(!bad)
		print("COWTEST-OK %d iterations\n", n);
	exits(bad ? "bad" : nil);
}
