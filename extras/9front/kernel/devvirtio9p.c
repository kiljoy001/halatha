/*
 * virtio 1.0 9P transport (#9) for 9front: mount the host namespace
 * over a virtqueue, with no network or serial line in the path.
 *
 *	bind -a '#9' /dev
 *	mount -c '#9/0' /n/host
 *
 * Ported from the 9legacy driver (extras/9legacy/kernel/devvirtio9p.c).
 * The device plumbing uses 9front's port/virtio10.h: registers are Vio
 * handles, so a BAR may be port or memory space, and virtiomapregs,
 * vin/vout come from pc/virtio10pc.c. 9front's pci scan already sizes
 * the BARs of a class 0x00 device, so the driver does not.
 *
 * The wire is plain 9P2000 end to end: the peer is v86's Virtio9pHandler,
 * a raw frame relay to wanix's fs/p9p server. There is no 9P2000.u shim;
 * every request is forwarded verbatim and every reply delivered as the
 * device wrote it.
 *
 * A virtio-9p device has one request queue. Each descriptor chain is one
 * 9P RPC: a device-readable buffer holding the T-message, then a
 * device-writable buffer the device fills with the R-message. devmnt
 * drives this chan like a tcp connection: it write()s one whole T-message
 * and reads replies back as a byte stream reassembled by the size[4]
 * prefix. So write() posts one chain, read() serves one complete reply at
 * a time, and the interrupt harvests completions onto a FIFO.
 */
#include "u.h"
#include "../port/lib.h"
#include "mem.h"
#include "dat.h"
#include "fns.h"
#include "io.h"
#include "../port/pci.h"
#include "ureg.h"
#include "../port/error.h"

#include "../port/virtio10.h"

typedef struct Vqueue Vqueue;
typedef struct Slot Slot;
typedef struct Ctlr Ctlr;

enum {
	Typ9p	= 9,		/* virtio device type for 9P */

	/* descriptor flags */
	Next	= 1,
	Write	= 2,

	VringSize	= 4,

	Fmounttag	= 1,	/* feature bit 0: device config carries a mount tag */

	/*
	 * One RPC in either direction: devmnt's MAXRPC in 9front
	 * (port/devmnt.c). A larger T-message is refused in v9write; the
	 * device can write at most this much of a reply.
	 */
	Msize	= IOHDRSZ+32768,
	Maxslot	= 64,		/* cap on outstanding RPCs per device */
};

struct Vqueue {
	Lock;

	Vio	notify;
	int	idx;
	int	size;

	int	free;		/* descriptor free list head */
	int	nfree;

	Vdesc	*desc;

	Vring	*avail;
	u16int	*availent;
	u16int	*availevent;

	Vring	*used;
	Vused	*usedent;
	u16int	*usedevent;
	u16int	lastused;

	void	*rock[];	/* head index -> Slot* in flight */
};

struct Slot {
	uchar	*out;		/* device-readable: the T-message */
	uchar	*in;		/* device-writable: the R-message */
	ulong	inlen;		/* bytes the device wrote */
	ulong	rp;		/* read-serve offset within in */
	Slot	*next;		/* free list / done FIFO link */
};

struct Ctlr {
	Pcidev	*pci;
	Vio	cfg;
	Vio	isr;
	Vio	notify;
	u32int	notifyoffmult;
	ulong	feat[2];

	Vqueue	*vq;		/* the single request queue */
	char	tag[64];	/* mount tag, for identification */

	Slot	*slot;
	int	nslot;
	Slot	*sfree;		/* free slots */
	Slot	*donehd;	/* completed replies, oldest first */
	Slot	*donetl;
	Slot	*cur;		/* reply being served by read (reader-private) */

	Rendez	rwait;		/* read waits here for a completed reply */
	Rendez	wwait;		/* write waits here for a free slot */

	int	started;	/* DriverOk + interrupt enabled */
	int	inuse;		/* one mount at a time */

	Ctlr	*next;
};

static Ctlr	*ctlrs;
static int	probed;

static void	v9interrupt(Ureg*, void*);

static Vqueue*
mkvqueue(int size)
{
	Vqueue *q;
	uchar *p;
	int i;

	q = malloc(sizeof(*q) + sizeof(void*)*size);
	p = mallocalign(
		PGROUND(sizeof(Vdesc)*size +
			VringSize +
			sizeof(u16int)*size +
			sizeof(u16int)) +
		PGROUND(VringSize +
			sizeof(Vused)*size +
			sizeof(u16int)),
		BY2PG, 0, 0);
	if(p == nil || q == nil){
		print("virtio9p: no memory for Vqueue\n");
		free(p);
		free(q);
		return nil;
	}

	q->desc = (void*)p;
	p += sizeof(Vdesc)*size;
	q->avail = (void*)p;
	p += VringSize;
	q->availent = (void*)p;
	p += sizeof(u16int)*size;
	q->availevent = (void*)p;
	p += sizeof(u16int);

	p = (uchar*)PGROUND((uintptr)p);
	q->used = (void*)p;
	p += VringSize;
	q->usedent = (void*)p;
	p += sizeof(Vused)*size;
	q->usedevent = (void*)p;

	q->free = -1;
	q->nfree = q->size = size;
	for(i=0; i<size; i++){
		q->desc[i].next = q->free;
		q->free = i;
	}

	return q;
}

static int
matchvirtiocfgcap(Pcidev *p, int cap, int off, int typ)
{
	int bar;

	if(cap != 9 || pcicfgr8(p, off+3) != typ)
		return 1;

	/* skip invalid bars */
	bar = pcicfgr8(p, off+4);
	if(bar < 0 || bar >= nelem(p->mem)
	|| p->mem[bar].size == 0)
		return 1;

	return 0;
}

static int
virtiocap(Pcidev *p, int typ)
{
	return pcienumcaps(p, matchvirtiocfgcap, typ);
}

/* the mount tag: device config (cap type 4) is taglen[2] then tag[taglen] */
static void
readtag(Ctlr *c)
{
	Vio dc;
	int i, n;

	c->tag[0] = 0;
	if((c->feat[0] & Fmounttag) == 0)
		return;
	if(virtiomapregs(c->pci, virtiocap(c->pci, 4), 0, &dc) == nil)
		return;
	n = vin16(&dc, 0);
	if(n >= sizeof(c->tag))
		n = sizeof(c->tag)-1;
	for(i = 0; i < n; i++)
		c->tag[i] = vin8(&dc, 2+i);
	c->tag[n] = 0;
}

static void
v9probe(void)
{
	Ctlr *c, **tail;
	Pcidev *p;
	Vqueue *q;
	Slot *s;
	int cap, n, i, ndev;

	if(probed)
		return;
	probed = 1;

	ndev = 0;
	tail = &ctlrs;
	for(p = nil; p = pcimatch(p, 0x1AF4, 0x1040+Typ9p);){
		if(p->rid == 0)		/* legacy/transitional; we want modern */
			continue;
		if((cap = virtiocap(p, 1)) < 0)
			continue;
		if((c = malloc(sizeof(*c))) == nil){
			print("virtio9p: no memory for Ctlr\n");
			break;
		}
		c->pci = p;
		if(virtiomapregs(p, cap, Vconf_sz, &c->cfg) == nil){
			free(c);
			continue;
		}
		pcienable(p);

		if(virtiomapregs(p, virtiocap(p, 3), 0, &c->isr) == nil){
Baddev:
			pcidisable(p);
			free(c);
			continue;
		}
		cap = virtiocap(p, 2);
		if(virtiomapregs(p, cap, 0, &c->notify) == nil)
			goto Baddev;
		c->notifyoffmult = pcicfgr32(p, cap+16);

		/* reset, then acknowledge */
		coherence();
		vout8(&c->cfg, Vconf_status, 0);
		while(vin8(&c->cfg, Vconf_status) != 0)
			delay(1);
		vout8(&c->cfg, Vconf_status, Sacknowledge|Sdriver);

		/* negotiate features: VERSION_1 and the mount tag */
		vout32(&c->cfg, Vconf_devfeatsel, 1);
		c->feat[1] = vin32(&c->cfg, Vconf_devfeat);
		vout32(&c->cfg, Vconf_devfeatsel, 0);
		c->feat[0] = vin32(&c->cfg, Vconf_devfeat);
		vout32(&c->cfg, Vconf_drvfeatsel, 1);
		vout32(&c->cfg, Vconf_drvfeat, c->feat[1] & Fversion1);
		vout32(&c->cfg, Vconf_drvfeatsel, 0);
		vout32(&c->cfg, Vconf_drvfeat, c->feat[0] & Fmounttag);
		vout8(&c->cfg, Vconf_status, vin8(&c->cfg, Vconf_status) | Sfeaturesok);
		if((vin8(&c->cfg, Vconf_status) & Sfeaturesok) == 0){
			print("virtio9p: device rejected features\n");
			goto Baddev;
		}

		/* the single request queue (index 0) */
		vout16(&c->cfg, Vconf_queuesel, 0);
		n = vin16(&c->cfg, Vconf_queuesize);
		if(n == 0 || (n & (n-1)) != 0){
			print("virtio9p: bad queue size %d\n", n);
			goto Baddev;
		}
		if((q = mkvqueue(n)) == nil)
			goto Baddev;
		q->idx = 0;
		q->notify = c->notify;
		if(q->notify.type == Vio_port)
			q->notify.port += c->notifyoffmult * vin16(&c->cfg, Vconf_queuenotifyoff);
		else
			q->notify.mem += c->notifyoffmult * vin16(&c->cfg, Vconf_queuenotifyoff);
		c->vq = q;
		coherence();
		vout64(&c->cfg, Vconf_queuedesc, PADDR(q->desc));
		vout64(&c->cfg, Vconf_queueavail, PADDR(q->avail));
		vout64(&c->cfg, Vconf_queueused, PADDR(q->used));

		readtag(c);

		/*
		 * A pool of request slots, each with a T- and an R-buffer.
		 * nslot <= size/2 so a free slot always has two descriptors.
		 */
		c->nslot = n/2;
		if(c->nslot > Maxslot)
			c->nslot = Maxslot;
		c->slot = malloc(sizeof(Slot) * c->nslot);
		if(c->slot == nil)
			goto Baddev;
		c->sfree = nil;
		for(i=0; i<c->nslot; i++){
			s = &c->slot[i];
			s->out = malloc(Msize);
			s->in = malloc(Msize);
			if(s->out == nil || s->in == nil){
				print("virtio9p: no memory for slots\n");
				goto Baddev;
			}
			s->next = c->sfree;
			c->sfree = s;
		}

		*tail = c;
		tail = &c->next;
		print("#9/%d: virtio9p tag \"%s\" nslot %d msize %d\n",
			ndev++, c->tag, c->nslot, Msize);
	}
}

static void
v9start(Ctlr *c)
{
	if(c->started)
		return;
	pcisetbme(c->pci);
	intrenable(c->pci->intl, v9interrupt, c, c->pci->tbdf, "virtio9p");
	coherence();
	vout16(&c->cfg, Vconf_queuesel, 0);
	vout16(&c->cfg, Vconf_queueenable, 1);
	vout8(&c->cfg, Vconf_status, vin8(&c->cfg, Vconf_status) | Sdriverok);
	c->started = 1;
}

/*
 * Harvest completed chains: recycle descriptors, queue replies, wake
 * readers. The used index wraps at 16 bits, so compare all of it: masking
 * with size-1 would miss a completion burst of exactly a full queue.
 */
static void
vqharvest(Ctlr *c)
{
	Vqueue *q;
	Slot *s;
	int id, free, m, ix;

	q = c->vq;
	m = q->size - 1;
	ilock(q);
	while((q->lastused ^ q->used->idx) & 0xFFFF){
		ix = q->lastused & m;
		id = q->usedent[ix].id;
		s = q->rock[id];
		q->rock[id] = nil;
		if(s != nil){
			s->inlen = q->usedent[ix].len;
			if(s->inlen > Msize)	/* never serve past our buffer */
				s->inlen = Msize;
		}
		q->lastused++;
		do {
			free = id;
			id = q->desc[free].next;
			q->desc[free].next = q->free;
			q->free = free;
			q->nfree++;
		} while(q->desc[free].flags & Next);
		if(s != nil){
			s->next = nil;
			if(c->donetl != nil)
				c->donetl->next = s;
			else
				c->donehd = s;
			c->donetl = s;
		}
	}
	iunlock(q);
	wakeup(&c->rwait);
}

static void
v9interrupt(Ureg*, void *arg)
{
	Ctlr *c = arg;

	if(vin8(&c->isr, 0) & 1)
		vqharvest(c);
}

static int
havereply(void *arg)
{
	return ((Ctlr*)arg)->donehd != nil;
}

static int
haveslot(void *arg)
{
	return ((Ctlr*)arg)->sfree != nil;
}

/* take a free slot, blocking until one is available */
static Slot*
getslot(Ctlr *c)
{
	Vqueue *q;
	Slot *s;

	q = c->vq;
	ilock(q);
	while(c->sfree == nil){
		iunlock(q);
		while(waserror())
			;
		tsleep(&c->wwait, haveslot, c, 1000);
		poperror();
		vqharvest(c);
		ilock(q);
	}
	s = c->sfree;
	c->sfree = s->next;
	s->next = nil;
	iunlock(q);
	return s;
}

static void
putslot(Ctlr *c, Slot *s)
{
	Vqueue *q;

	q = c->vq;
	ilock(q);
	s->next = c->sfree;
	c->sfree = s;
	iunlock(q);
	wakeup(&c->wwait);
}

/* dequeue the oldest completed reply, blocking until one arrives */
static Slot*
getreply(Ctlr *c)
{
	Vqueue *q;
	Slot *s;

	q = c->vq;
	ilock(q);
	while(c->donehd == nil){
		iunlock(q);
		while(waserror())
			;
		tsleep(&c->rwait, havereply, c, 1000);
		poperror();
		if(c->donehd == nil)
			vqharvest(c);		/* in case the interrupt was missed */
		ilock(q);
	}
	s = c->donehd;
	c->donehd = s->next;
	if(c->donehd == nil)
		c->donetl = nil;
	iunlock(q);
	s->next = nil;
	return s;
}

static void
submit(Ctlr *c, Slot *s, long outlen)
{
	Vqueue *q;
	Vdesc *d;
	int head, free;

	q = c->vq;
	ilock(q);
	head = free = q->free;
	d = &q->desc[free]; free = d->next;
	d->addr = PADDR(s->out);
	d->len = outlen;
	d->flags = Next;
	d = &q->desc[free]; free = d->next;
	d->addr = PADDR(s->in);
	d->len = Msize;
	d->flags = Write;
	q->free = free;
	q->nfree -= 2;
	q->rock[head] = s;
	q->availent[q->avail->idx & (q->size-1)] = head;
	coherence();
	q->avail->idx++;
	iunlock(q);
	if((q->used->flags & 1) == 0)
		vout16(&q->notify, 0, q->idx);
}

/* drop any queued/in-progress replies back to the free list */
static void
v9flush(Ctlr *c)
{
	Vqueue *q;
	Slot *s, *nx;

	q = c->vq;
	ilock(q);
	if(c->cur != nil){
		c->cur->next = c->sfree;
		c->sfree = c->cur;
		c->cur = nil;
	}
	for(s = c->donehd; s != nil; s = nx){
		nx = s->next;
		s->next = c->sfree;
		c->sfree = s;
	}
	c->donehd = c->donetl = nil;
	iunlock(q);
}

static Ctlr*
ctlrindex(int i)
{
	Ctlr *c;

	for(c = ctlrs; c != nil && i > 0; c = c->next, i--)
		;
	return c;
}

static int
v9gen(Chan *c, char*, Dirtab*, int, int s, Dir *dp)
{
	Qid q;
	char buf[16];

	if(s == DEVDOTDOT){
		mkqid(&q, 0, 0, QTDIR);
		devdir(c, q, "#9", 0, eve, DMDIR|0555, dp);
		return 1;
	}
	if(ctlrindex(s) == nil)
		return -1;
	snprint(buf, sizeof buf, "%d", s);
	mkqid(&q, s+1, 0, QTFILE);
	devdir(c, q, buf, 0, eve, 0660, dp);
	return 1;
}

static void
v9reset(void)
{
	v9probe();
}

static Chan*
v9attach(char *spec)
{
	return devattach('9', spec);
}

static Walkqid*
v9walk(Chan *c, Chan *nc, char **name, int nname)
{
	return devwalk(c, nc, name, nname, nil, 0, v9gen);
}

static int
v9stat(Chan *c, uchar *db, int n)
{
	return devstat(c, db, n, nil, 0, v9gen);
}

static Chan*
v9open(Chan *c, int omode)
{
	Ctlr *cl;
	Vqueue *q;

	if(c->qid.type & QTDIR){
		if(omode != OREAD)
			error(Eperm);
		c->mode = openmode(omode);
		c->flag |= COPEN;
		c->offset = 0;
		return c;
	}

	cl = ctlrindex(c->qid.path - 1);
	if(cl == nil)
		error(Enonexist);

	q = cl->vq;
	ilock(q);
	if(cl->inuse){
		iunlock(q);
		error(Einuse);
	}
	cl->inuse = 1;
	iunlock(q);
	if(waserror()){
		ilock(q);
		cl->inuse = 0;
		iunlock(q);
		nexterror();
	}
	v9start(cl);
	v9flush(cl);		/* start the conversation clean */
	poperror();

	c->aux = cl;
	c->mode = openmode(omode);
	c->flag |= COPEN;
	c->offset = 0;
	return c;
}

static void
v9close(Chan *c)
{
	Ctlr *cl;
	Vqueue *q;

	if((c->flag & COPEN) == 0 || (c->qid.type & QTDIR))
		return;
	cl = c->aux;
	if(cl == nil)
		return;
	v9flush(cl);
	q = cl->vq;
	ilock(q);
	cl->inuse = 0;
	iunlock(q);
}

static long
v9read(Chan *c, void *a, long n, vlong)
{
	Ctlr *cl;
	Slot *s;
	long k;

	if(c->qid.type & QTDIR)
		return devdirread(c, a, n, nil, 0, v9gen);
	if(n <= 0)
		return 0;

	cl = c->aux;
	s = cl->cur;
	if(s == nil){
		s = getreply(cl);
		s->rp = 0;
		cl->cur = s;
	}
	k = s->inlen - s->rp;
	if(k > n)
		k = n;
	memmove(a, s->in + s->rp, k);
	s->rp += k;
	if(s->rp >= s->inlen){
		cl->cur = nil;
		putslot(cl, s);
	}
	return k;
}

/*
 * One write is one whole T-message. Its length is bounded by the slot's
 * buffer: devmnt never sends more than MAXRPC, but the chan can be opened
 * and written by any process, so the bound is checked, not assumed.
 */
static long
v9write(Chan *c, void *a, long n, vlong)
{
	Ctlr *cl;
	Slot *s;

	if(c->qid.type & QTDIR)
		error(Eperm);
	if(n < BIT32SZ+BIT8SZ+BIT16SZ)
		error("invalid 9P message");
	if(n > Msize)
		error(Etoobig);

	cl = c->aux;
	s = getslot(cl);
	memmove(s->out, a, n);
	submit(cl, s, n);
	return n;
}

Dev virtio9pdevtab = {
	'9',
	"virtio9p",

	v9reset,
	devinit,
	devshutdown,
	v9attach,
	v9walk,
	v9stat,
	v9open,
	devcreate,
	v9close,
	v9read,
	devbread,
	v9write,
	devbwrite,
	devremove,
	devwstat,
};
