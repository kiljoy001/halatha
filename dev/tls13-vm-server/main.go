// A small TLS 1.3-only server for the isolated 9front VM pair test.
package main

import (
	"crypto/tls"
	"io"
	"log"
)

func main() {
	cert, err := tls.LoadX509KeyPair("/n/9fat/halatha/server.crt", "/n/9fat/halatha/server.key")
	if err != nil {
		log.Fatal(err)
	}
	listener, err := tls.Listen("tcp", "10.44.0.1:16443", &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS13,
		MaxVersion:   tls.VersionTLS13,
	})
	if err != nil {
		log.Fatal(err)
	}
	defer listener.Close()
	log.Print("TLS13 PEER LISTEN")
	for {
		conn, err := listener.Accept()
		if err != nil {
			log.Fatal(err)
		}
		go func() {
			defer conn.Close()
			buf := make([]byte, 4096)
			if _, err := conn.Read(buf); err != nil && err != io.EOF {
				return
			}
			body := "VM PEER TLS13 PASS\n"
			_, _ = io.WriteString(conn,
				"HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\n"+
					"Content-Length: 19\r\nConnection: close\r\n\r\n"+body)
		}()
	}
}
