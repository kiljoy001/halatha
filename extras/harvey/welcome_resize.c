/* Resize notification for the O9C welcome window.
 *
 * libdraw calls eresized() when the window changes size; the event loop
 * polls this flag and redraws.  Linked only into bin/welcome.
 */
#include <u.h>
#include <libc.h>
#include <draw.h>
#include <event.h>

int welcome_resized;

void
eresized(int new)
{
	if(new && getwindow(display, Refnone) < 0)
		return;
	welcome_resized = 1;
}
