#include <u.h>
#include <libc.h>
#include <draw.h>
#include <thread.h>
#include <ttf.h>

int mainstacksize = 128*1024;

static void
check(int ok, char *message)
{
    if(!ok){
        fprint(2, "TTF-CHECK FAIL: %s: %r\n", message);
        threadexitsall("font check");
    }
}

void
threadmain(int argc, char **argv)
{
    TTFont *tt;
    TTGlyph *g;
    TTShapeInfo shape[3];
    TTBitmap colour;
    Rune ffi[] = {'f', 'f', 'i', 0};
    Rune marked[] = {'A', 0x0301, 0};
    Font *small, *large;
    Image *im, *colourimage;
    Point p;
    uchar *pixels;
    char capabilities[128];
    char longtext[162];
    int i, n, fd, ink, sw, lw, dx, dy;
    USED(argc); USED(argv);
    print("TTF-CHECK begin\n");
    tt = ttfopen("/lib/font/ttf/Aspera.ttf", 32, 0);
    check(tt != nil, "standalone ttfopen");
    g = ttfgetglyph(tt, ttffindchar(tt, 'A'), 1);
    check(g != nil && g->bit != nil, "standalone rasterization");
    ink = 0;
    for(i = 0; i < g->stride*g->height; i++) ink += g->bit[i] != 0;
    check(ink > 0, "standalone glyph has pixels");
    print("libttf raster PASS ink=%d\n", ink);
    ttfputglyph(g);
    n = ttfshape(tt, ffi, 3, shape);
    check(n > 0 && n < 3, "ffi ligature (test expects DejaVu Sans)");
    print("libttf GSUB PASS glyphs=%d\n", n);
    check(ttfmarkpos(tt->u, ttffindchar(tt, 0x0301), ttffindchar(tt, 'A'), 0, &dx, &dy), "GPOS mark attachment");
    print("libttf GPOS PASS offset=%d,%d\n", dx, dy);
    ttfclose(tt);
    tt = ttfopen("/lib/font/ttf/ColourTest.ttf", 32, 0);
    check(tt != nil, "colour fixture open");
    memset(&colour, 0, sizeof colour);
    check(ttfcolrglyph(tt, ttffindchar(tt, 'A'), &colour) > 0, "COLRv1 rasterization");
    ink = 0;
    for(i = 0; i < colour.stride*colour.height; i += 4)
        if(colour.bit[i] > 128 && colour.bit[i+1] == 0 && colour.bit[i+2] == 0 && colour.bit[i+3] > 128) ink++;
    check(ink > 20, "COLRv1 red pixels");
    free(colour.bit);
    ttfclose(tt);
    print("libttf COLRv1 PASS red=%d\n", ink);
    fd = open("/n/ttf/Aspera.ttf.24/shapeable", OREAD);
    check(fd >= 0, "font service visible in client namespace");
    n = read(fd, capabilities, sizeof capabilities-1);
    close(fd);
    check(n > 0, "font service capabilities");
    capabilities[n] = 0;
    print("truetypefs capabilities %s\n", capabilities);
    check(initdraw(nil, nil, "Aspera TrueType") >= 0, "draw connection");
    print("draw connection PASS\n");
    fd = open("/n/ttf/ColourTest.ttf.32/glyph/0041", OREAD);
    check(fd >= 0, "colour glyph service open");
    im = readimage(display, fd, 0);
    close(fd);
    check(im != nil && im->chan == ABGR32, "colour image through 9P");
    n = bytesperline(im->r, im->depth)*Dy(im->r);
    pixels = malloc(n);
    check(pixels != nil && unloadimage(im, im->r, pixels, n) == n, "colour image pixels");
    ink = 0;
    for(i = 0; i < n; i += 4)
        if(pixels[i] > 128 && pixels[i+1] == 0 && pixels[i+2] == 0 && pixels[i+3] > 128) ink++;
    check(ink > 20, "9P preserves colour pixels");
    free(pixels);
    colourimage = im;
    print("truetypefs COLRv1 PASS red=%d\n", ink);
    small = openfont(display, "/n/ttf/ColourTest.ttf.32/font");
    check(small != nil && stringwidth(small, "A") > 0, "colour-capable libdraw font");
    check(small->cacheimage != nil && small->cacheimage->chan == ABGR32, "libdraw colour capability cache");
    freefont(small);
    print("libdraw colour capability PASS\n");
    small = openfont(display, "/n/ttf/Aspera.ttf.24/font");
    large = openfont(display, "/n/ttf/Aspera.ttf.36/font");
    check(small != nil && large != nil, "libdraw openfont through 9P");
    sw = stringwidth(small, "Aspera ffi");
    lw = stringwidth(large, "Aspera ffi");
    print("font metrics: %d %d cluster=%d\n", sw, lw, nextclusterlen(small, ffi, 3));
    check(sw > 0 && lw > sw, "scalable font metrics");
    check(nextclusterlen(small, ffi, 3) == 3, "libdraw shaping hook");
    check(nextclusterlen(small, marked, 2) == 2, "libdraw combining mark cluster");
    check(stringnwidth(small, "ffi", 1) == stringwidth(small, "f"), "bounded UTF-8 width");
    check(runestringnwidth(small, ffi, 1) == runestringwidth(small, (Rune*)L"f"), "bounded rune width");
    memmove(longtext, "xx", 2);
    for(i = 0; i < 40; i++) memmove(longtext+2+4*i, "ffi ", 4);
    longtext[161] = 0;
    check(stringwidth(small, longtext) == stringwidth(small, "xx") + 40*stringwidth(small, "ffi") + 39*stringwidth(small, " "), "ligatures across cache batches");
    print("libdraw clusters and bounded widths PASS\n");
    im = allocimage(display, Rect(0, 0, 400, 120), XRGB32, 0, DWhite);
    check(im != nil, "test image allocation");
    draw(im, rectaddpt(colourimage->r, Pt(350, 4)), colourimage, nil, ZP);
    freeimage(colourimage);
    p = string(im, Pt(4, 4), display->black, ZP, small, "Aspera ffi");
    check(p.x == 4+sw, "draw advance equals measured width");
    string(im, Pt(4, 48), display->black, ZP, large, "Aspera ffi");
    pixels = malloc(400*120*4);
    check(pixels != nil, "pixel buffer");
    n = unloadimage(im, im->r, pixels, 400*120*4);
    check(n == 400*120*4, "read rendered pixels");
    ink = 0;
    for(i = 0; i < n; i += 4)
        if(pixels[i] < 128 && pixels[i+1] < 128 && pixels[i+2] < 128) ink++;
    check(ink > 100, "rendered text has ink");
    draw(screen, screen->r, display->white, nil, ZP);
    draw(screen, rectaddpt(im->r, addpt(screen->r.min, Pt(8, 8))), im, nil, ZP);
    flushimage(display, 1);
    print("libdraw TTF PASS widths=%d,%d ink=%d\n", sw, lw, ink);
    free(pixels);
    freeimage(im);
    freefont(small);
    freefont(large);
    print("TTF-CHECK PASS\n");
    threadexitsall(nil);
}
