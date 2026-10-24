// Package ninepuri parses the user-space 9P locator grammar.
//
// The kernel never sees these strings. A resolver turns a URI into a
// transport dial string (or a kernel channel) and mounts the resulting
// 9P service:
//
//	9p://host!port/aname
//	9p+il://[200::1]!564/aname
//	9p+tcp://host!564/aname
//	9p+tag://host9p/aname
//	9p+ring://0/aname
//
// The authority for il/tcp is "host!port"; port defaults to 564 (9fs).
// The path is the 9P attach name. Query parameters may override the
// attach name (aname=...), the service name (name=...), or authentication
// (auth=none|factotum).
package ninepuri

import (
	"fmt"
	"net/url"
	"strings"
)

// Transport is the 9P carrier named by a URI.
type Transport string

const (
	TransportTCP  Transport = "tcp"
	TransportIL   Transport = "il"
	TransportTag  Transport = "tag"
	TransportRing Transport = "ring"
)

// URI is a parsed 9P locator.
type URI struct {
	Raw       string
	Scheme    string
	Transport Transport
	Host      string
	Port      string
	Aname     string
	Query     url.Values
}

// Parse parses a 9P locator. It deliberately does not resolve names or
// open anything; that is the mount helper's job.
func Parse(raw string) (*URI, error) {
	i := strings.Index(raw, "://")
	if i < 0 {
		return nil, fmt.Errorf("missing :// in %q", raw)
	}
	scheme := raw[:i]
	rest := raw[i+3:]

	u := &URI{Raw: raw, Scheme: scheme, Query: url.Values{}}

	switch scheme {
	case "9p":
		u.Transport = TransportTCP
	case "9p+il":
		u.Transport = TransportIL
	case "9p+tcp":
		u.Transport = TransportTCP
	case "9p+tag":
		u.Transport = TransportTag
	case "9p+ring":
		u.Transport = TransportRing
	default:
		return nil, fmt.Errorf("unsupported 9P scheme %q", scheme)
	}

	j := strings.IndexAny(rest, "/?")
	var authority, tail string
	if j < 0 {
		authority, tail = rest, ""
	} else {
		authority, tail = rest[:j], rest[j:]
	}
	if authority == "" {
		return nil, fmt.Errorf("%s: empty authority", raw)
	}

	if k := strings.IndexByte(tail, '?'); k >= 0 {
		q, err := url.ParseQuery(tail[k+1:])
		if err != nil {
			return nil, fmt.Errorf("%s: bad query: %w", raw, err)
		}
		u.Query = q
		tail = tail[:k]
	}
	u.Aname = tail

	switch u.Transport {
	case TransportIL, TransportTCP:
		host, port, err := splitHostPort(authority)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", raw, err)
		}
		u.Host, u.Port = host, port
	case TransportTag, TransportRing:
		if strings.ContainsAny(authority, "/!") {
			return nil, fmt.Errorf("%s: bad %s name %q", raw, u.Transport, authority)
		}
		u.Host = authority
	}

	if a := u.Query.Get("aname"); a != "" {
		u.Aname = a
	}
	return u, nil
}

func splitHostPort(authority string) (host, port string, err error) {
	port = "564"
	if strings.HasPrefix(authority, "[") {
		end := strings.IndexByte(authority, ']')
		if end < 0 {
			return "", "", fmt.Errorf("unterminated IPv6 literal in %q", authority)
		}
		host = authority[1:end]
		rest := authority[end+1:]
		switch {
		case rest == "":
		case strings.HasPrefix(rest, "!"):
			port = rest[1:]
		default:
			return "", "", fmt.Errorf("bad authority suffix %q", rest)
		}
	} else if i := strings.LastIndexByte(authority, '!'); i >= 0 {
		host, port = authority[:i], authority[i+1:]
	} else {
		host = authority
	}
	if host == "" || port == "" {
		return "", "", fmt.Errorf("missing host or port in %q", authority)
	}
	return host, port, nil
}

// DialString returns the string accepted by Plan 9 dial/srv, or a
// kernel channel path for tag/ring transports.
func (u *URI) DialString() string {
	switch u.Transport {
	case TransportIL:
		return "il!" + u.Host + "!" + u.Port
	case TransportTCP:
		return "tcp!" + u.Host + "!" + u.Port
	case TransportTag:
		return "#9/" + u.Host
	case TransportRing:
		return "#~/" + u.Host
	default:
		return ""
	}
}

// ServiceName returns a stable, filesystem-safe /srv name.
func (u *URI) ServiceName() string {
	base := "9p"
	switch u.Transport {
	case TransportIL, TransportTCP:
		base = "9p-" + u.Host + "-" + u.Port
	case TransportTag:
		base = "9p-tag-" + u.Host
	case TransportRing:
		base = "9p-ring-" + u.Host
	}
	return strings.Map(func(r rune) rune {
		switch r {
		case ':', '%', '/', '[', ']', '!':
			return '_'
		}
		if r <= ' ' || r == 0x7f {
			return -1
		}
		return r
	}, base)
}

// NoAuth reports whether the URI asks for an unauthenticated mount.
// The default is noauth for the small, local 9P services this tool was
// written for; auth=factotum opts into the normal amount path.
func (u *URI) NoAuth() bool {
	switch strings.ToLower(u.Query.Get("auth")) {
	case "factotum", "yes", "true", "1":
		return false
	}
	return true
}

// String returns the canonical form of the locator.
func (u *URI) String() string {
	var b strings.Builder
	b.WriteString("9p+")
	b.WriteString(string(u.Transport))
	b.WriteString("://")
	switch u.Transport {
	case TransportIL, TransportTCP:
		if strings.Contains(u.Host, ":") {
			b.WriteByte('[')
			b.WriteString(u.Host)
			b.WriteByte(']')
		} else {
			b.WriteString(u.Host)
		}
		b.WriteByte('!')
		b.WriteString(u.Port)
	default:
		b.WriteString(u.Host)
	}
	b.WriteString(u.Aname)
	return b.String()
}
