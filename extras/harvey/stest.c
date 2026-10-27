/* stest: sbrk chunks must be strictly increasing and non-overlapping,
 * and must retain their contents. Arena overlap corrupts malloc. */
#include <u.h>
#include <libc.h>

void
main(int argc, char **argv)
{
	int kid = argc > 1 && strcmp(argv[1], "kid") == 0;
	int rounds = 200, i, j;
	char *ch[512];
	Waitmsg *w;

	if(kid){
		char *brk0 = sbrk(0);
		for(i = 0; i < rounds; i++){
			char *p = sbrk(8192);
			if(p == (void*)-1)
				exits("sbrk");
			for(j = 0; j < i; j++){
				if(p < ch[j] + 8192 && ch[j] < p + 8192){
					print("STEST-OVERLAP chunk %d at %p vs chunk %d at %p brk0 %p\n",
						i, p, j, ch[j], brk0);
					exits("overlap");
				}
			}
			ch[i] = p;
			memset(p, (i * 37 + 11) & 0xff, 8192);
		}
		for(i = 0; i < rounds; i++){
			u8 want = (i * 37 + 11) & 0xff;
			for(j = 0; j < 8192; j++){
				if((u8)ch[i][j] != want){
					print("STEST-CORRUPT chunk %d off %d got 0x%02x want 0x%02x\n",
						i, j, (u8)ch[i][j], want);
					exits("corrupt");
				}
			}
		}
		exits(nil);
	}
	if(argc > 1)
		rounds = atoi(argv[1]);
	for(i = 0; i < rounds; i++){
		int pid = fork();
		if(pid == 0){
			char *a[] = { "test", "kid", nil };
			exec("/bin/test", a);
			exits("exec");
		}
		w = wait();
		if(w == nil || w->msg[0] != '\0'){
			print("STEST kid bad round %d msg '%s'\n", i, w ? w->msg : "?");
			exits("kid");
		}
		free(w);
	}
	print("STEST-DONE ok\n");
	exits(nil);
}
