// Command share-folder is a small MSFP host: one server, its records in
// JSON files, paired by hand with another. It will share a folder's
// films once the engine has its catalog; for now it pairs.
//
// Two of them on one machine:
//
//	go run . -address https://127.0.0.1:8441 -listen :8441 -data a -loopback
//	go run . -address https://127.0.0.1:8442 -listen :8442 -data b -loopback
//
// Then type "code" in the first, "redeem <code>" in the second, and
// "accept <id>" in the first. "peers" lists them, "end <id>" ends one.
// "rotate" moves this server to its next key; "stolen" revokes its key
// and rotates ("stolen none": without the next key, every pairing ends);
// "confirm <id>" accepts a peer's new key after comparing fingerprints.
package main

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"flag"
	"fmt"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/overfin/server/msfp"
	"github.com/overfin/server/msfp/store/jsonfile"
	"github.com/overfin/server/msfp/wire"
)

func main() {
	address := flag.String("address", "", "this server's address, https://host[:port]")
	listen := flag.String("listen", ":8443", "where to listen")
	data := flag.String("data", "msfp-data", "the directory of its records")
	loopback := flag.Bool("loopback", false, "allow peers on this machine")
	flag.Parse()
	ctx := context.Background()

	store, err := jsonfile.Open(*data)
	if err != nil {
		log.Fatal(err)
	}
	cert := selfSigned(*address)
	eng, err := msfp.Open(ctx, msfp.Config{Address: *address, Store: store, AllowLoopback: *loopback,
		Pins: []string{wire.Pin(cert.Leaf)}, Notifier: printer{}})
	if err != nil {
		log.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/api/v1/federation/", eng.FederationHandler())
	srv := &http.Server{Addr: *listen, Handler: mux, TLSConfig: &tls.Config{Certificates: []tls.Certificate{cert}}}
	go func() { log.Fatal(srv.ListenAndServeTLS("", "")) }()
	go eng.Run(ctx)

	me, _ := eng.Server(ctx)
	fmt.Println("this server:", me.Address, me.Fingerprint)
	in := bufio.NewScanner(os.Stdin)
	for fmt.Print("> "); in.Scan(); fmt.Print("> ") {
		cmd, arg, _ := strings.Cut(strings.TrimSpace(in.Text()), " ")
		switch cmd {
		case "code":
			code, _, err := eng.MakeCode(ctx, arg)
			show(code, err)
		case "redeem":
			p, err := eng.RedeemCode(ctx, arg)
			show(fmt.Sprintf("waiting for %s's operator; its fingerprint: %s", p.Name, p.Fingerprint), err)
		case "accept":
			p, err := eng.Accept(ctx, arg)
			show("paired with "+p.Name, err)
		case "end":
			show("ended", eng.End(ctx, arg))
		case "rotate":
			show("key changed; peers told", eng.Rotate(ctx, arg))
		case "stolen":
			rotated, err := eng.KeyStolen(ctx, strings.TrimPrefix(arg, "none"), arg == "none")
			show(fmt.Sprintf("key revoked; rotated to the next key: %v", rotated), err)
		case "confirm":
			p, err := eng.ConfirmRotation(ctx, arg)
			show("new key confirmed: "+p.Fingerprint, err)
		case "peers":
			eng.PanelOpen()
			peers, err := eng.Peers(ctx)
			for _, p := range peers {
				fmt.Printf("%s  %-7s %s  %s  %s", p.ID, p.State, p.Fingerprint, p.Name, p.Address)
				if p.Pending != nil {
					fmt.Print("  new key to confirm: ", p.Pending.Fingerprint)
				}
				fmt.Println()
			}
			show("", err)
		default:
			fmt.Println("code [note] | redeem <code> | accept <id> | end <id> | peers | rotate [key] | stolen [key|none] | confirm <id>")
		}
	}
}

func show(ok string, err error) {
	if err != nil {
		fmt.Println("error:", err)
	} else if ok != "" {
		fmt.Println(ok)
	}
}

// printer prints the engine's events.
type printer struct{}

func (printer) Notify(e msfp.Event) {
	if w, ok := e.(msfp.PeerWaiting); ok {
		fmt.Printf("\n%s used your code; its fingerprint is %s. Compare, then: accept %s\n", w.Name, w.Fingerprint, w.Peer)
		return
	}
	fmt.Printf("\n%T %+v\n", e, e)
}

// selfSigned is a certificate for address's host, made at each start:
// its pin is named in this server's hellos, so peers accept it.
func selfSigned(address string) tls.Certificate {
	u, err := url.Parse(address)
	if err != nil {
		log.Fatal(err)
	}
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()),
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(365 * 24 * time.Hour)}
	if ip := net.ParseIP(u.Hostname()); ip != nil {
		tmpl.IPAddresses = []net.IP{ip}
	} else {
		tmpl.DNSNames = []string{u.Hostname()}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, pub, priv)
	if err != nil {
		log.Fatal(err)
	}
	leaf, _ := x509.ParseCertificate(der)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: priv, Leaf: leaf}
}
