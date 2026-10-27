/* rtest: realloc growth must preserve contents, under malloc churn
 * with rc-like allocation sizes (64-byte nodes, string sizes). */
#include <u.h>
#include <libc.h>

static int hdrbad;
static void
chkfree(void *p)
{
	u32 *u = (u32*)p;
	if(p == nil)
		return;
	while(u[-1] == 0xA1F1D1C1u) u--;
	if(u[-2] != 0x0A110C09u && u[-2] != (0xCAB00D1Eu + 1) && u[-2] != 0xBA5EBA11u){
		if(hdrbad < 5)
			print("RTEST-HDRBAD p=%p hm2=0x%lux hm1=0x%lux\n", p, (ulong)u[-2], (ulong)u[-1]);
		hdrbad++;
	}
	free(p);
}

static u32
pat(int i, int seed)
{
	return (u32)(seed * 1000003 + i * 2654435761u);
}

void
main(int argc, char **argv)
{
	int rounds = 300, r, i, bad = 0;
	void *churn[64];

	if(argc > 1)
		rounds = atoi(argv[1]);
	for(r = 0; r < rounds; r++){
		u32 *p = malloc(100 * sizeof(u32));
		int sz = 100;
		if(p == nil)
			exits("malloc");
		for(i = 0; i < sz; i++)
			p[i] = pat(i, r);
		/* churn: rc-sized blocks around the realloc */
		for(i = 0; i < 64; i++)
			churn[i] = malloc(64);
		for(i = 0; i < 64; i += 2)
			chkfree(churn[i]);
		while(sz < 1600){
			sz *= 2;
			p = realloc(p, sz * sizeof(u32));
			if(p == nil)
				exits("realloc");
			for(i = 0; i < sz / 2; i++){
				if(p[i] != pat(i, r)){
					print("RTEST-CORRUPT round %d sz %d idx %d got 0x%lux want 0x%lux\n",
						r, sz, i, (ulong)p[i], (ulong)pat(i, r));
					bad++;
					break;
				}
			}
			for(i = sz / 2; i < sz; i++)
				p[i] = pat(i, r);
		}
		for(i = 0; i < sz; i++){
			if(p[i] != pat(i, r)){
				print("RTEST-FINAL-CORRUPT round %d idx %d\n", r, i);
				bad++;
				break;
			}
		}
		chkfree(p);
		for(i = 1; i < 64; i += 2)
			chkfree(churn[i]);
	}
	print("RTEST-DONE rounds %d bad %d hdrbad %d\n", rounds, bad, hdrbad);
	exits(nil);
}
