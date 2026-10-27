"""Apply reply-lifetime fixes to the current Objective-9C guest toolchain.

The slow browser CPU exposes a race: receiving a buffered reply can resume the
caller before sendp returns to the actor. The caller must not release the
pending entry's channel reference, and the actor must not reread the caller's
message after publishing the reply.
"""
import re


def runtime_source(source):
    if 'o9_pending_take(' in source:
        return source
    source, count = re.subn(
        r'(r = recvp\(w->replyc\);\n)        o9_pending_complete\(w->replyc\);\n',
        r'\1', source)
    if count != 4:
        raise RuntimeError('Objective-9C synchronous reply code changed; review compatibility fix')
    source = source.replace(
        'reply = recvp((Channel*)op->replyc);\n\t\t\to9_pending_complete((Channel*)op->replyc);',
        'reply = recvp((Channel*)op->replyc);')
    before = '''\t} else if(msg->sync != nil){
\t\tif(msg->replyc != nil)
\t\t\tsendp(msg->replyc, r);
\t\to9_pending_complete(msg->replyc);
'''
    after = '''\t} else if(msg->sync != nil){
\t\tChannel *replyc = msg->replyc;
\t\t/* The receiver may free msg as soon as sendp publishes the reply. */
\t\tif(replyc != nil)
\t\t\tsendp(replyc, r);
\t\to9_pending_complete(replyc);
'''
    if source.count(before) != 1:
        raise RuntimeError('Objective-9C reply dispatcher changed; review compatibility fix')
    return source.replace(before, after)


def generated_source(source):
    source, shelf_count = re.subn(r'pid = rfork\(RFPROC\|RFFDG\|RFNAMEG\|RFNOTEG\);\n\s*if\(pid == 0\)\{.*?\n\s*\}',
                    'pid = halatha_start_shelf(path);', source, flags=re.S)
    source, terminal_count = re.subn(r'pid = rfork\(RFPROC\|RFFDG\|RFNAMEG\|RFENVG\|RFNOTEG\|RFNOWAIT\);\n\s*if\(pid == 0\)\{.*?_exits\("rc exec"\);\n\s*\}',
                    'pid = halatha_start_terminal(in[0], in[1], output);', source, flags=re.S)
    if 'o9_impl_AsperaShelfStartRaw_run' in source and shelf_count != 1:
        raise RuntimeError('Shelf launch code changed; review compatibility fix')
    if 'o9_impl_AsperaTermStartRaw_run' in source and terminal_count != 1:
        raise RuntimeError('Terminal launch code changed; review compatibility fix')
    # Cache the selector before calling the method. Its reply wakes the caller,
    # which owns and frees synchronous messages. Spawn messages remain actor-owned.
    source = source.replace('switch(m->sel){', 'ulong completed_selector = m->sel;\n\t\tswitch(completed_selector){')
    return source.replace('self->__spawn_state == 1 && m->sel ==',
                          'self->__spawn_state == 1 && completed_selector ==')
