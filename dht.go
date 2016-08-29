package main

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"log"
	"math/big"
	"net"
	"net/rpc"
	"time"

	"github.com/nictuku/dht"
)

func init () {
	if err := rpc.Register(new(RPC)); err != nil {
		log.Fatalf(`can't register RPC: %s`, err)
	}
}

type PeerManager struct {
	d *dht.DHT
	cert tls.Certificate
	tlsconf *tls.Config
}

func NewPeerManager() PeerManager {
	pm := PeerManager{}

	pm.setupCertificate()

	pm.tlsconf = &tls.Config{
		InsecureSkipVerify: true,
		Certificates: []tls.Certificate{ pm.cert },
	}

	return pm
}

func (pm *PeerManager) Loop() {
	/* XXX: Autogenerate? */
	ih, err := dht.DecodeInfoHash("6d062837ce8d379e5c808f10b4ad70a678e96a8a")
	if err != nil {
		log.Printf(`can't decode infohash: %s`, err)
		return
	}

	conf := dht.NewConfig()
	conf.Port = 55000

	pm.d, err = dht.New(conf)
	if err != nil {
		log.Printf(`can't create DHT: %s`, err)
		return
	}

	if err = pm.d.Start(); err != nil {
		log.Printf(`can't start DHT: %s`, err)
		return
	}

	go pm.drainPeers()
	go pm.handleIncoming()

	for {
		pm.d.PeersRequest(string(ih), true)
		time.Sleep(5 * time.Second)
	}
}

func (pm *PeerManager) setupCertificate() {
	template := x509.Certificate{
		SerialNumber: big.NewInt(4711),
		Subject: pkix.Name{
			Organization: []string{"unobtanium"},
			CommonName: "*",
		},
		DNSNames: []string{ `*` },
		NotAfter:              time.Now().AddDate(0, 0, 10),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
		SignatureAlgorithm:    x509.SHA512WithRSA,
		PublicKeyAlgorithm:    x509.ECDSA,
		SubjectKeyId:          []byte{1, 2, 3, 4, 5},
		BasicConstraintsValid: true,
		IsCA: true,
	}

	log.Printf(`generating 2048 bit RSA private key`)
	priv, _ := rsa.GenerateKey(rand.Reader, 2048)
	pub := &priv.PublicKey
	log.Printf(`building self-signed certificate`)
	cert_raw, err := x509.CreateCertificate(rand.Reader, &template, &template, pub, priv)
	if err != nil {
		log.Fatalf(`can't generate TLS certificate: %s`, err)
	}
	log.Printf(`created a cert of length %d bytes`, len(cert_raw))

	privbuf := &bytes.Buffer{}
	pem.Encode(privbuf, &pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(priv)})

	certbuf := &bytes.Buffer{}
	pem.Encode(certbuf, &pem.Block{Type: "CERTIFICATE", Bytes: cert_raw})

	cert, err := tls.X509KeyPair(certbuf.Bytes(), privbuf.Bytes())
	if err != nil {
		log.Fatalf(`can't build key pair: %s`, err)
	}

	pm.cert = cert
}

func (pm *PeerManager) drainPeers() {
	log.Printf(`draining DHT`)
	seen := make(map[string]struct{})

	for r := range pm.d.PeersRequestResults {
		for _, peers := range r {
			for _, x := range peers {
				addr := dht.DecodePeerAddress(x)
				if _, ok := seen[addr]; !ok {
					go pm.connectToPeer(addr)
				}
				seen[addr] = struct{}{}
			}
		}
	}
}

func (pm *PeerManager) handleIncoming() {
	log.Printf(`DHT bound to port %d`, pm.d.Port())

	ln, err := tls.Listen("tcp", fmt.Sprintf(":%d", pm.d.Port()), pm.tlsconf)
	if err != nil {
		log.Printf(`can't open listening socket: %s`, err)
		return
	}
	defer ln.Close()

	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Printf(`accept() failed: %s`, err)
			continue
		}

		go rpc.ServeConn(conn)
	}
}

func (pm *PeerManager) connectToPeer(addr string) {
	log.Printf(`got a new peer: %s`, addr)

	conn, err := tls.Dial("tcp", addr, pm.tlsconf)
	if err != nil {
		log.Printf(`can't dial %s: %s`, addr, err)
		return
	}
	go pm.talkToPeer(conn)
}

type RPC bool
type Result struct {
	Payload string
}
type Args struct {
	Name string
}

func (pm *PeerManager) talkToPeer(conn net.Conn) {
	defer conn.Close()

	log.Printf(`connected to %s, local addr: %s`, conn.RemoteAddr(), conn.LocalAddr())

	rpcClient := rpc.NewClient(conn)
	res := &Result{}
	if err := rpcClient.Call("RPC.Hello", &Args{"foo"}, &res); err != nil {
		log.Printf(`failed to call RPC: %s`, err)
		return
	}

	log.Printf(`got RPC result: %s`, res.Payload)
}

func (r *RPC) Hello(args *Args, res *Result) error {
	log.Printf(`Received a hello`)

	res.Payload = fmt.Sprintf("Hi %s!", args.Name)

	return nil
}
