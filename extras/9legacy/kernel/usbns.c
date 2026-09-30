/*
 * usbns - a software-only USB host-controller interface that bridges the
 * 9legacy USB stack to the browser WebUSB service exposed by the direct
 * page as 9P files under /n/host/usb/. v86 emulates no USB controller, so
 * the real HCDs (uhci/ohci/ehci) find no PCI device and usbd dies at boot
 * with "usbd: /dev/usb: no hubs". usbns registers a root hub with devusb
 * via addhcitype("ns", reset) so usbd runs; root-hub port control
 * (GET_STATUS/SET_FEATURE on ports) is intercepted by devusb's toy-hub
 * rhubread/rhubwrite and routed to our portstatus/portenable/portreset.
 * Non-root device transfers are forwarded to the page by writing/reading
 * /n/host/usb/<id>/{req,resp} through the existing /n/host mount (the
 * in-kernel devmnt 9P client; we must NOT open #9/0 a second time -- the
 * virtio-9p server rejects a second session with Einuse).
 *
 * Single-device proof of concept: NsCtlr caches one page-side device id.
 * When no WebUSB device is open, usb/devices is empty and portstatus
 * reports 0, so usbd idles with an empty root hub. When the page opens a
 * device (startUsbService -> openDevice), it lists the device in
 * usb/devices and creates usb/<id>/{req,resp,ctl,status,info}; on the
 * next usbd poll (Pollms=250ms) portstatus returns HPpresent|HPenable
 * and usbd enumerates the device through us.
 *
 * Transfer contract (mirrors usbuhci): for control, devusb calls epwrite
 * once with buf[0..8)=8-byte setup + buf[8..n)=OUT data (h2d only), then
 * epread once to drain device-to-host data. For bulk/intr, epwrite is
 * always OUT (raw bytes) and epread always IN. devusb IGNORES epwrite's
 * return value (usbwrite returns the original n), so failures MUST raise
 * error(Eio), not return -1. Root-hub ep0 control never reaches
 * epread/epwrite (rhubread/rhubwrite return >=0 and short-circuit).
 *
 * Invariants (load-bearing):
 *   1. reset() succeeds once only (static done guard) or the probe loop
 *      fills all 16 hcis[] slots and starves the real HCDs.
 *   2. error(Eio) on transfer failure, never a bare -1.
 *   3. init=nil -- it runs at boot with no /n/host mounted yet.
 *   4. No ilock across a 9P RPC: the RPC sleeps on mountrpc; a per-device
 *      QLock (sleepable) serializes transfers since req/resp are shared
 *      across all endpoints of the device.
 */
#include "u.h"
#include "../port/lib.h"
#include "mem.h"
#include "dat.h"
#include "fns.h"
#include "io.h"
#include "../port/error.h"
#include "../port/usb.h"

typedef struct NsCtlr NsCtlr;
typedef struct NsEp NsEp;

struct NsCtlr
{
	QLock;		/* serializes all transfers on the device: req/resp are per-device, not per-ep */
	char id[16];	/* page-side device id (first token of usb/devices); "" = none */
};

struct NsEp
{
	Chan *req;	/* /n/host/usb/<id>/req, ORDWR; nil for root-hub ep0 */
	Chan *resp;	/* /n/host/usb/<id>/resp, OREAD; nil for root-hub ep0 */
	uchar *data;	/* staged control-IN (d2h) bytes for a later epread */
	int ndata;	/* -1 request expected; 0 EOF; >0 bytes staged */
	int isroot;	/* 1 for root-hub ep0 (devusb intercepts control) */
};

static char *devlistpath = "/n/host/usb/devices";

/* hex helpers: 9legacy print.c has no %H verb (only devuart.c has a private one). */

static int
hexval(int c)
{
	if(c >= '0' && c <= '9') return c - '0';
	if(c >= 'a' && c <= 'f') return c - 'a' + 10;
	if(c >= 'A' && c <= 'F') return c - 'A' + 10;
	return -1;
}

static int
ishex(char *s)
{
	if(s == nil || *s == 0)
		return 0;
	for(; *s; s++)
		if(hexval(*s) < 0)
			return 0;
	return 1;
}

static int
dec_hex(char *h, uchar *out, int maxout)
{
	int n, hi, lo;

	n = 0;
	while(h[0] && h[1] && n < maxout){
		hi = hexval(h[0]);
		lo = hexval(h[1]);
		if(hi < 0 || lo < 0)
			return -1;
		out[n++] = (hi << 4) | lo;
		h += 2;
	}
	return n;
}

static void
tohex(uchar *p, int n, char *dst)
{
	static const char hd[] = "0123456789abcdef";
	int i;

	for(i = 0; i < n; i++){
		dst[i*2] = hd[p[i] >> 4];
		dst[i*2+1] = hd[p[i] & 0xf];
	}
	dst[n*2] = '\0';
}

/*
 * nsrpc - send a request line to the page-side bridge and collect the
 * response. The caller holds the device QLock. Writes req to the req
 * file at offset 0, polls req until the page clears it (it clears req
 * only after writing resp), then reads and parses resp at offset 0.
 * For control/bulk IN, the hex payload in resp is decoded into out
 * (<=maxout bytes). Returns the data length (>=0) or -1 on
 * error/timeout. Decimal values are emitted without leading zeros
 * (the page's parseInt(t,0) treats leading-0 as octal).
 *
 * resp forms (page runReq):
 *   IN:   ok <len> <hex> <status>   (4 tokens; hex is lowercase, no separators)
 *   OUT:  ok <len> <status>        (3 tokens; len=0 for control, payload for bulk)
 *   err:  err <msg>
 */
static long
nsrpc(NsEp *e, char *req, uchar *out, int maxout)
{
	Chan *reqc, *respc;
	int bufsz, n, i, ntok, len, dec;
	char *buf, *tok[8], *p, c;

	reqc = e->req;
	respc = e->resp;
	if(reqc == nil || respc == nil)
		return -1;

	bufsz = maxout*2 + 64;
	buf = smalloc(bufsz);
	if(waserror()){
		free(buf);
		nexterror();
	}

	/* write the request line at offset 0 (req is empty from the previous cycle) */
	devtab[reqc->type]->write(reqc, req, strlen(req), 0);

	/* poll req until the page clears it (handled + wrote resp) */
	for(i = 0; i < 250; i++){	/* ~5s at 20ms */
		n = devtab[reqc->type]->read(reqc, &c, 1, 0);
		if(n <= 0)
			break;
		tsleep(&up->sleep, return0, 0, 20);
	}
	if(i >= 250){
		poperror();
		free(buf);
		return -1;
	}

	/* read resp at offset 0 */
	n = devtab[respc->type]->read(respc, buf, bufsz-1, 0);
	if(n < 0){
		poperror();
		free(buf);
		return -1;
	}
	buf[n] = '\0';

	/* tokenize in place */
	ntok = 0;
	for(p = buf; *p && ntok < nelem(tok); ){
		while(*p==' ' || *p=='\t' || *p=='\n' || *p=='\r')
			*p++ = '\0';
		if(*p == 0)
			break;
		tok[ntok++] = p;
		while(*p && !(*p==' ' || *p=='\t' || *p=='\n' || *p=='\r'))
			p++;
	}

	if(ntok == 0 || strcmp(tok[0], "err") == 0 || strcmp(tok[0], "ok") != 0){
		poperror();
		free(buf);
		return -1;
	}
	len = (ntok > 1) ? atoi(tok[1]) : 0;
	if(ntok >= 4 && ishex(tok[2])){
		dec = -1;
		if(out != nil && maxout > 0)
			dec = dec_hex(tok[2], out, maxout);
		if(dec < 0 && out != nil && maxout > 0){
			poperror();
			free(buf);
			return -1;
		}
		if(dec >= 0 && dec < len)
			len = dec;	/* decoded fewer than the declared length */
	}
	poperror();
	free(buf);
	return len;
}

static void
nsepopen(Ep *ep)
{
	NsCtlr *ctlr;
	NsEp *e;
	char path[64];

	e = smalloc(sizeof(NsEp));
	if(ep->dev->isroot && ep->nb == 0){
		/* usbd opens the root-hub ep0 data file; control is handled by
		   devusb's rhubread/rhubwrite, so just carry a marker. */
		e->isroot = 1;
		ep->aux = e;
		return;
	}
	e->isroot = 0;
	ctlr = ep->hp->aux;
	if(ctlr == nil || ctlr->id[0] == 0)
		error(Enodev);

	if(waserror()){
		if(e->req != nil) cclose(e->req);
		if(e->resp != nil) cclose(e->resp);
		free(e->data);
		free(e);
		nexterror();
	}
	snprint(path, sizeof(path), "/n/host/usb/%s/req", ctlr->id);
	e->req = namec(path, Aopen, ORDWR, 0);
	e->req->flag &= ~CCACHE;		/* fresh Tread each poll (termrc mounts -c) */
	snprint(path, sizeof(path), "/n/host/usb/%s/resp", ctlr->id);
	e->resp = namec(path, Aopen, OREAD, 0);
	e->resp->flag &= ~CCACHE;
	poperror();
	ep->aux = e;
}

static void
nsepclose(Ep *ep)
{
	NsEp *e;

	e = ep->aux;
	if(e == nil)
		return;		/* usbuhci panics on nil aux here; usbns must not */
	ep->aux = nil;
	if(e->req != nil)
		cclose(e->req);
	if(e->resp != nil)
		cclose(e->resp);
	free(e->data);
	free(e);
}

static long
nsepread(Ep *ep, void *a, long n)
{
	NsCtlr *ctlr;
	NsEp *e;
	char *req;
	long rv, count;

	if(ep->aux == nil)
		panic("usbns: epread: not open");
	e = ep->aux;
	if(e->isroot)
		panic("usbns: epread on root hub ep0");	/* devusb intercepts; should not happen */
	ctlr = ep->hp->aux;
	qlock(ctlr);
	if(waserror()){
		qunlock(ctlr);
		nexterror();
	}
	switch(ep->ttype){
	case Tctl:
		/* control EOF state machine, mirrors usbuhci epread */
		if(e->ndata < 0)
			error("request expected");
		if(e->ndata == 0){
			e->ndata = -1;
			count = 0;
		}else{
			count = n;
			if(count > e->ndata)
				count = e->ndata;
			if(count > 0)
				memmove(a, e->data, count);
			free(e->data);
			e->data = nil;
			e->ndata = 0;		/* EOF next read */
		}
		qunlock(ctlr);
		poperror();
		return count;
	case Tbulk:
	case Tintr:
		/* bulk/intr IN: bare endpoint number, request n bytes */
		req = smalloc(32);
		if(waserror()){
			free(req);
			nexterror();
		}
		snprint(req, 32, "in %ud %ld", (uint)ep->nb, n);
		rv = nsrpc(e, req, a, n);
		poperror();
		free(req);
		if(rv < 0)
			error(Eio);
		qunlock(ctlr);
		poperror();
		return rv;
	default:
		error("usbns: bad ep ttype");
	}
	return -1;	/* not reached */
}

static long
nsepwrite(Ep *ep, void *a, long n)
{
	NsCtlr *ctlr;
	NsEp *e;
	uchar *c;
	char *req;
	long wlen, datalen, rv;
	int bm, reqlen;

	if(ep->aux == nil)
		panic("usbns: epwrite: not open");
	e = ep->aux;
	if(e->isroot)
		panic("usbns: epwrite on root hub ep0");	/* devusb intercepts; should not happen */
	ctlr = ep->hp->aux;
	qlock(ctlr);
	if(waserror()){
		qunlock(ctlr);
		nexterror();
	}
	switch(ep->ttype){
	case Tctl:
		if(n < Rsetuplen)
			error("short usb command");
		c = a;
		bm = c[Rtype];
		/* reset staged control state for this new request */
		free(e->data);
		e->data = nil;
		e->ndata = 0;
		if(bm & Rd2h){
			/* control IN: page reads wlen bytes from the device */
			wlen = GET2(c+Rcount);
			if(wlen < 0)
				wlen = 0;
			if(wlen > Maxctllen)
				error("d2h data too large");
			req = smalloc(64);
			if(waserror()){
				free(req);
				nexterror();
			}
			snprint(req, 64, "control %ud %ud %ud %ud %ud",
				(uint)bm, (uint)c[Rreq],
				(uint)GET2(c+Rvalue), (uint)GET2(c+Rindex), (uint)wlen);
			e->data = smalloc(wlen+1);
			rv = nsrpc(e, req, e->data, wlen);
			poperror();
			free(req);
			if(rv < 0)
				error(Eio);
			e->ndata = rv;
			qunlock(ctlr);
			poperror();
			return Rsetuplen;
		}
		/* control OUT: send OUT data after the setup */
		datalen = n - Rsetuplen;
		reqlen = 64 + datalen*2;
		req = smalloc(reqlen);
		if(waserror()){
			free(req);
			nexterror();
		}
		if(datalen > 0){
			snprint(req, reqlen, "control %ud %ud %ud %ud %ud ",
				(uint)bm, (uint)c[Rreq],
				(uint)GET2(c+Rvalue), (uint)GET2(c+Rindex), (uint)datalen);
			tohex(c + Rsetuplen, datalen, req + strlen(req));
		}else{
			snprint(req, reqlen, "control %ud %ud %ud %ud %ud",
				(uint)bm, (uint)c[Rreq],
				(uint)GET2(c+Rvalue), (uint)GET2(c+Rindex), 0);
		}
		rv = nsrpc(e, req, nil, 0);
		poperror();
		free(req);
		if(rv < 0)
			error(Eio);
		qunlock(ctlr);
		poperror();
		return n;		/* Rsetuplen + datalen */
	case Tbulk:
	case Tintr:
		/* bulk/intr OUT: raw bytes, bare endpoint number */
		datalen = n;
		reqlen = 32 + datalen*2;
		req = smalloc(reqlen);
		if(waserror()){
			free(req);
			nexterror();
		}
		snprint(req, reqlen, "out %ud ", (uint)ep->nb);
		tohex((uchar*)a, datalen, req + strlen(req));
		rv = nsrpc(e, req, nil, 0);
		poperror();
		free(req);
		if(rv < 0)
			error(Eio);
		qunlock(ctlr);
		poperror();
		return n;
	default:
		error("usbns: bad ep ttype");
	}
	return -1;	/* not reached */
}

static int
nsportstatus(Hci *hp, int port)
{
	NsCtlr *ctlr;
	Chan *dc;
	char buf[256], *p, *id;
	int n;

	USED(port);
	ctlr = hp->aux;
	if(ctlr == nil)
		return 0;
	dc = nil;
	if(waserror()){
		if(dc != nil)
			cclose(dc);
		return 0;		/* tolerate: /n/host not mounted yet, or transient */
	}
	dc = namec(devlistpath, Aopen, OREAD, 0);
	dc->flag &= ~CCACHE;
	n = devtab[dc->type]->read(dc, buf, sizeof(buf)-1, 0);
	cclose(dc);
	dc = nil;
	poperror();
	if(n <= 0)
		return 0;
	buf[n] = '\0';
	/* first token of the first non-empty line is the page-side device id */
	id = nil;
	for(p = buf; *p; ){
		while(*p==' ' || *p=='\t' || *p=='\n' || *p=='\r')
			p++;
		if(*p == 0)
			break;
		id = p;
		while(*p && !(*p==' ' || *p=='\t' || *p=='\n' || *p=='\r'))
			p++;
		if(*p)
			*p++ = '\0';
		if(id[0])
			break;
	}
	if(id == nil || id[0] == 0)
		return 0;
	strncpy(ctlr->id, id, sizeof(ctlr->id)-1);
	ctlr->id[sizeof(ctlr->id)-1] = '\0';
	return HPpresent | HPenable;	/* full-speed PoC */
}

static int
nsportenable(Hci *hp, int port, int on)
{
	USED(hp); USED(port); USED(on);
	return 1;			/* no-op success */
}

static int
nsportreset(Hci *hp, int port, int on)
{
	USED(hp); USED(port); USED(on);
	return 1;			/* no-op success */
}

static void
nsdump(Hci *hp)
{
	USED(hp);
}

static char *
nsseprintep(char *s, char *e, Ep *ep)
{
	return seprint(s, e, "usbns ep%d.%d", ep->dev->nb, ep->nb);
}

static void
nsdebug(Hci *hp, int on)
{
	USED(hp); USED(on);
}

static void
nsshutdown(Hci *hp)
{
	USED(hp);
}

static int
nsreset(Hci *hp)
{
	static int done;
	NsCtlr *ctlr;

	if(done)
		return -1;		/* once only: don't fill all 16 hcis[] slots */
	done = 1;

	hp->nports = 1;
	hp->highspeed = 0;
	hp->tbdf = BUSUNKNOWN;
	hp->irq = 0;
	hp->interrupt = nil;		/* intrenable(nil handler) just warns; usbd polls every Pollms */

	ctlr = smalloc(sizeof(NsCtlr));
	ctlr->id[0] = 0;
	hp->aux = ctlr;

	hp->init = nil;		/* runs at boot with no /n/host mounted; must not do 9P */
	hp->dump = nsdump;
	hp->epopen = nsepopen;
	hp->epclose = nsepclose;
	hp->epread = nsepread;
	hp->epwrite = nsepwrite;
	hp->seprintep = nsseprintep;
	hp->portenable = nsportenable;
	hp->portreset = nsportreset;
	hp->portstatus = nsportstatus;
	hp->shutdown = nsshutdown;
	hp->debug = nsdebug;
	return 0;
}

void
usbnslink(void)
{
	addhcitype("ns", nsreset);
}
