// Run on a Yggdrasil-connected host to smoke-test the proposal-only 9P service.
// It generates an ephemeral public key and does not approve or enroll it.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"time"

	"9fans.net/go/plan9"
	"9fans.net/go/plan9/client"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: test-ygg-request-e2e SERVER_YGG_IP CLIENT_TRANSPORT_PUBLIC_KEY")
		os.Exit(2)
	}
	address := net.ParseIP(os.Args[1])
	transport, err := hex.DecodeString(os.Args[2])
	if address == nil || address.To4() != nil || address.To16()[0] != 2 || err != nil || len(transport) != ed25519.PublicKeySize {
		panic("expected a Yggdrasil address and a 32-byte public key")
	}
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		panic(err)
	}
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(address.String(), "17026"), 8*time.Second)
	if err != nil {
		panic(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(15 * time.Second))
	p9, err := client.NewConn(conn)
	if err != nil {
		panic(err)
	}
	fs, err := p9.Attach(nil, "none", "")
	if err != nil {
		panic(err)
	}
	if _, err := fs.Stat("config.json"); err == nil {
		panic("administrative configuration exposed")
	}
	request, err := fs.Open("request", plan9.OWRITE|plan9.OTRUNC)
	if err != nil {
		panic(err)
	}
	body, _ := json.Marshal(struct {
		Action       string `json:"action"`
		ClientKey    string `json:"clientKey"`
		TransportKey string `json:"transportKey"`
	}{"register", hex.EncodeToString(public), os.Args[2]})
	if _, err := request.Write(body); err != nil {
		panic(err)
	}
	request.Close()
	status, err := fs.Open("status", plan9.OREAD)
	if err != nil {
		panic(err)
	}
	defer status.Close()
	var receipt struct {
		ID               string `json:"id"`
		ClientKey        string `json:"clientKey"`
		ObservedPeer     string `json:"observedPeer"`
		IdentityVerified bool   `json:"identityVerified"`
	}
	if err := json.NewDecoder(status).Decode(&receipt); err != nil {
		panic(err)
	}
	if receipt.ID == "" || receipt.ClientKey != hex.EncodeToString(public) || receipt.IdentityVerified || net.ParseIP(receipt.ObservedPeer) == nil {
		panic("invalid or falsely verified receipt")
	}
	fmt.Printf("proposal id=%s client=%s observed=%s verified=false\n", receipt.ID, receipt.ClientKey, receipt.ObservedPeer)
}
