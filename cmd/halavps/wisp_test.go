package main

import (
	"net"
	"testing"
)

func TestWispAllowPolicy(t *testing.T) {
	cases := []struct {
		name string
		addr string
		env  string
		ip   string
		want bool
	}{
		{"local allows public", "127.0.0.1:8099", "", "1.1.1.1", true},
		{"local allows private", "127.0.0.1:8099", "", "192.168.1.10", true},
		{"local blocks loopback", "127.0.0.1:8099", "", "127.0.0.1", false},
		{"local blocks metadata", "127.0.0.1:8099", "", "169.254.169.254", false},
		{"public bind denies public", "0.0.0.0:8099", "", "1.1.1.1", false},
		{"public bind allows private", "0.0.0.0:8099", "", "10.0.0.5", true},
		{"explicit list wins", "127.0.0.1:8099", "10.0.0.0/8", "1.1.1.1", false},
		{"explicit any", "0.0.0.0:8099", "any", "1.1.1.1", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("HALAVPS_NET_ALLOW", c.env)
			w := newWispServer(&server{}, c.addr)
			if got := w.allowed(net.ParseIP(c.ip)); got != c.want {
				t.Fatalf("%s: allowed=%v want %v", c.ip, got, c.want)
			}
		})
	}
}
