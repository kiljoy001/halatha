/* Spawn exec children through libthread's scheduler stack. A raw rfork from
 * an actor runs on a heap-backed thread stack; its COW snapshot can race with
 * the other actors sharing that heap. procexec performs the fork on the
 * process's private stack and reports completion through an exec pipe. */
#include <u.h>
#include <libc.h>
#include <thread.h>
#include "aspera_exec.h"

typedef struct Start Start;
struct Start {
    Channel *pid;
    char *path;
    int terminal;
    int input;
    int writer;
    int output;
};

static void
startproc(void *arg)
{
    Start *start = arg;
    Channel *pid = start->pid;
    char *argv[] = {start->path, start->terminal ? "-i" : nil, nil};
    if(start->terminal){
        if(bind("#e", "/env", MREPL|MCREATE) < 0 ||
           bind("#d", "/fd", MREPL) < 0)
            goto failed;
        close(start->writer);
        dup(start->input, 0);
        dup(start->output, 1);
        dup(start->output, 2);
        if(start->input > 2) close(start->input);
        if(start->output > 2) close(start->output);
    }
    /* Successful procexec sends the PID and never returns. */
    procexec(pid, start->path, argv);
    threadexits("exec failed");
failed:
    sendul(pid, ~0UL);
    threadexits("namespace failed");
}

static int
start(Start *args)
{
    int tid, pid;
    /* Harvey's sendul/recvul wire elements are 32-bit even on amd64. */
    args->pid = chancreate(sizeof(uint), 1);
    if(args->pid == nil)
        return -1;
    tid = procrfork(startproc, args, 65536, RFFDG|RFNAMEG|RFENVG|RFNOTEG);
    pid = tid < 0 ? -1 : (int)recvul(args->pid);
    chanfree(args->pid);
    return pid;
}

int
halatha_start_shelf(char *path)
{
    Start args;
    Dir *service;
    int pid, tries;
    memset(&args, 0, sizeof args);
    args.path = path;
    pid = start(&args);
    if(pid < 0)
        return -1;
    /* exec completion precedes Objective-9C's class registration and post.
     * The browser CPU can take longer than ShelfClient's one-second poll.
     * Wait without spawning another actor for every existence check. */
    for(tries = 0; tries < 1200; tries++){
        service = dirstat("/srv/ShelfViews");
        if(service != nil){
            free(service);
            return pid;
        }
        sleep(25);
    }
    fprint(2, "halatha: ShelfViews was not published within 30 seconds\n");
    return -1;
}

int
halatha_start_terminal(int input, int writer, int output)
{
    Start args;
    memset(&args, 0, sizeof args);
    args.path = "/bin/rc";
    args.terminal = 1;
    args.input = input;
    args.writer = writer;
    args.output = output;
    return start(&args);
}
