package ninepuri

import "testing"

func TestParse(t *testing.T) {
	cases := []struct {
		raw    string
		tr     Transport
		host   string
		port   string
		aname  string
		dial   string
		noauth bool
	}{
		{
			raw:    "9p+il://[200:f359:ca2b:6a10:e30c:e663:6bd7:363d]!17026/",
			tr:     TransportIL,
			host:   "200:f359:ca2b:6a10:e30c:e663:6bd7:363d",
			port:   "17026",
			aname:  "/",
			dial:   "il!200:f359:ca2b:6a10:e30c:e663:6bd7:363d!17026",
			noauth: true,
		},
		{
			raw:    "9p+il://[200::1]!564/usr/scott",
			tr:     TransportIL,
			host:   "200::1",
			port:   "564",
			aname:  "/usr/scott",
			dial:   "il!200::1!564",
			noauth: true,
		},
		{
			raw:    "9p+il://200::1",
			tr:     TransportIL,
			host:   "200::1",
			port:   "564",
			dial:   "il!200::1!564",
			noauth: true,
		},
		{
			raw:    "9p+tcp://host!564/",
			tr:     TransportTCP,
			host:   "host",
			port:   "564",
			aname:  "/",
			dial:   "tcp!host!564",
			noauth: true,
		},
		{
			raw:    "9p://host!564/",
			tr:     TransportTCP,
			host:   "host",
			port:   "564",
			aname:  "/",
			dial:   "tcp!host!564",
			noauth: true,
		},
		{
			raw:    "9p+tag://host9p/",
			tr:     TransportTag,
			host:   "host9p",
			aname:  "/",
			dial:   "#9/host9p",
			noauth: true,
		},
		{
			raw:    "9p+ring://0/",
			tr:     TransportRing,
			host:   "0",
			aname:  "/",
			dial:   "#~/0",
			noauth: true,
		},
		{
			raw:    "9p+il://[200::1]!17026?aname=/data&auth=factotum",
			tr:     TransportIL,
			host:   "200::1",
			port:   "17026",
			aname:  "/data",
			dial:   "il!200::1!17026",
			noauth: false,
		},
	}
	for _, c := range cases {
		u, err := Parse(c.raw)
		if err != nil {
			t.Errorf("%s: %v", c.raw, err)
			continue
		}
		if u.Transport != c.tr || u.Host != c.host || u.Port != c.port || u.Aname != c.aname {
			t.Errorf("%s: got %s %q %q %q", c.raw, u.Transport, u.Host, u.Port, u.Aname)
		}
		if got := u.DialString(); got != c.dial {
			t.Errorf("%s: DialString() = %q, want %q", c.raw, got, c.dial)
		}
		if got := u.NoAuth(); got != c.noauth {
			t.Errorf("%s: NoAuth() = %v, want %v", c.raw, got, c.noauth)
		}
	}
}

func TestParseErrors(t *testing.T) {
	for _, raw := range []string{
		"",
		"http://host/",
		"9p+il://",
		"9p+il://[200::1",
		"9p+tag://a!b",
		"9p+il://[200::1]!564?aname=%zz",
	} {
		if _, err := Parse(raw); err == nil {
			t.Errorf("%q: expected error", raw)
		}
	}
}

func TestString(t *testing.T) {
	u, err := Parse("9p+il://[200::1]!564/usr/scott")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := u.String(), "9p+il://[200::1]!564/usr/scott"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}

func TestServiceName(t *testing.T) {
	u, err := Parse("9p+il://[200::1]!17026/")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := u.ServiceName(), "9p-200__1-17026"; got != want {
		t.Fatalf("ServiceName() = %q, want %q", got, want)
	}
}
