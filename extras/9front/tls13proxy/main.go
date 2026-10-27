// tls13proxy gives 9front programs a local TCP endpoint backed by verified
// TLS 1.3. It is a user-space bridge while 9front's libsec and /net/tls only
// implement the older record protocol.
package main

import (
	"crypto/tls"
	"crypto/x509"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"time"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:14443", "local TCP address")
	target := flag.String("target", "", "remote TLS address, host:port")
	serverName := flag.String("servername", "", "certificate DNS name")
	caFile := flag.String("ca", "", "PEM CA file; system trust is used if omitted")
	certFile := flag.String("cert", "", "PEM client certificate for mutual TLS")
	keyFile := flag.String("key", "", "PEM client private key for mutual TLS")
	once := flag.Bool("once", false, "serve one local connection and exit")
	flag.Parse()
	if (*certFile == "") != (*keyFile == "") {
		log.Fatal("-cert and -key must be given together")
	}

	localHost, _, err := net.SplitHostPort(*listen)
	if err != nil || net.ParseIP(localHost) == nil || !net.ParseIP(localHost).IsLoopback() {
		log.Fatal("-listen must be a numeric loopback address and port")
	}
	remoteHost, _, err := net.SplitHostPort(*target)
	if err != nil || remoteHost == "" {
		log.Fatal("-target must be host:port")
	}
	if *serverName == "" {
		*serverName = remoteHost
	}
	config := &tls.Config{
		MinVersion: tls.VersionTLS13,
		MaxVersion: tls.VersionTLS13,
		ServerName: *serverName,
	}
	if *caFile != "" {
		pem, err := os.ReadFile(*caFile)
		if err != nil {
			log.Fatal(err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			log.Fatal("-ca contains no PEM certificates")
		}
		config.RootCAs = pool
	}
	if *certFile != "" {
		cert, err := tls.LoadX509KeyPair(*certFile, *keyFile)
		if err != nil {
			log.Fatal(err)
		}
		config.Certificates = []tls.Certificate{cert}
	}
	listener, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatal(err)
	}
	defer listener.Close()
	log.Printf("TLS13 LISTEN %s", listener.Addr())
	for {
		client, err := listener.Accept()
		if err != nil {
			log.Fatal(err)
		}
		if *once {
			handle(client, *target, config)
			return
		}
		go handle(client, *target, config)
	}
}

func handle(client net.Conn, target string, config *tls.Config) {
	defer client.Close()
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	remote, err := tls.DialWithDialer(dialer, "tcp", target, config)
	if err != nil {
		log.Printf("TLS13 FAIL %v", err)
		return
	}
	defer remote.Close()
	if remote.ConnectionState().Version != tls.VersionTLS13 {
		log.Print("TLS13 FAIL negotiated an unexpected version")
		return
	}
	log.Printf("TLS13 CONNECTED %s", target)
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(remote, client)
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(client, remote)
		done <- struct{}{}
	}()
	<-done
	_ = client.Close()
	_ = remote.Close()
	<-done
	fmt.Fprintln(os.Stderr, "TLS13 CLOSED")
}
