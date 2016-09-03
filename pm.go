package main

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"log"
	"math/big"
	"strings"
	"sync"
	"time"

	"github.com/go-mangos/mangos"
	"github.com/go-mangos/mangos/protocol/bus"
	"github.com/go-mangos/mangos/transport/tlstcp"
	"github.com/nictuku/dht"
)

type BusMessage struct {
	From    string
	To      string
	Seq     int64
	Cmd     string
	Payload string
}

func NewBusMessage(data []byte, err error) (*BusMessage, error) {
	if err != nil {
		return nil, err
	}

	rv := &BusMessage{}
	if err = json.Unmarshal(data, rv); err != nil {
		return nil, err
	}
	return rv, nil
}

func (b *BusMessage) String() string {
	return fmt.Sprintf("[%s: %v (ID: %d)]", b.Cmd, b.Payload, b.Seq)
}

func (b *BusMessage) Bytes() []byte {
	m, err := json.Marshal(b)
	if err != nil {
		log.Fatalf(`can't marshall bus message to json: %s`, err)
	}
	return m
}

type Friend struct {
	name     string
	msg      *BusMessage
	lastSeen time.Time
	mtx      *sync.Mutex
	pm       *PeerManager
}

func NewFriend(name string, pm *PeerManager) *Friend {
	return &Friend{name: name, mtx: &sync.Mutex{}, lastSeen: time.Now(), pm: pm}
}

func (f *Friend) String() string {
	f.mtx.Lock()
	defer f.mtx.Unlock()

	if f.msg == nil {
		return fmt.Sprintf(`%s: no message, last seen: %s`, f.name, f.lastSeen)
	}

	return fmt.Sprintf("%s: last message: %s, last seen: %s ago", f.name, f.msg, time.Now().Sub(f.lastSeen))
}

func (f *Friend) HandleMessage(m *BusMessage) {
	f.mtx.Lock()
	defer f.mtx.Unlock()

	if f.msg != nil && f.msg.Seq >= m.Seq {
		/* We've already seen this message */
		return
	}

	f.msg = m
	f.lastSeen = time.Now()

	switch m.Cmd {
	case "event":
		ev := Event{}
		if err := json.Unmarshal([]byte(m.Payload), &ev); err != nil {
			log.Printf(`can't unmarshal event payload: %s`, err)
		}
		log.Printf(`got an event: %v`, ev)

		if err := f.pm.db.MergeEvent(ev); err != nil {
			log.Printf(`can't merge event: %s`, err)
		}
	case "hello":
		f.pm.mtx.Lock()
		defer f.pm.mtx.Unlock()

		f.pm.need_full_sync = true
	case "i'm alive":
		/* nothing */
	default:
		log.Printf(`unhandled message: tgt: %s, src: %s, cmd: %s, payload: %v`, m.To, m.From, m.Cmd, m.Payload)
	}
}

type PeerManager struct {
	d              *dht.DHT
	db             *DB
	bus            mangos.Socket
	nick           string
	friends        map[string]*Friend
	oldfriends     map[string]*Friend
	venue          string
	sequence       int64
	need_full_sync bool
	opts map[string]interface{}
	mtx            *sync.RWMutex
}

func NewPeerManager(db *DB) *PeerManager {
	conf := dht.NewConfig()
	conf.Port = 55000

	pm := PeerManager{db: db}

	pm.opts = map[string]interface{}{ mangos.OptionTLSConfig: pm.setupTLS() }

	var err error
	pm.bus, err = bus.NewSocket()
	if err != nil {
		log.Fatalf(`can't create BUS socket: %s`, err)
	}
	pm.bus.AddTransport(tlstcp.NewTransport())

	if pm.d, err = dht.New(conf); err != nil {
		log.Fatalf(`can't create DHT: %s`, err)
	}

	sum := sha1.Sum([]byte("LetsMeetHere"))
	pm.venue = hex.EncodeToString(sum[:])

	buf := make([]byte, 4)
	rand.Read(buf)
	pm.nick = hex.EncodeToString(buf)

	log.Printf(`My nickname is %s`, pm.nick)
	log.Printf(`I will meet my friends at %s`, pm.venue)

	pm.friends = make(map[string]*Friend)
	pm.oldfriends = make(map[string]*Friend)

	pm.mtx = &sync.RWMutex{}

	return &pm
}

func (pm *PeerManager) setupTLS() *tls.Config {
	cert := pm.setupCertificate()

	return &tls.Config{
		InsecureSkipVerify: true,
		Certificates:       []tls.Certificate{cert},
	}
}

func (pm *PeerManager) setupCertificate() tls.Certificate {
	template := x509.Certificate{
		SerialNumber: big.NewInt(4711),
		Subject: pkix.Name{
			Organization: []string{"unobtanium"},
			CommonName:   "*",
		},
		DNSNames:              []string{`*`},
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

	return cert
}

func (pm *PeerManager) String() string {
	pm.mtx.RLock()
	defer pm.mtx.RUnlock()

	s := []string{
		fmt.Sprintf("DHT: Port: %d", pm.d.Port()),
		fmt.Sprintf("\r\nI am %s, my sequence ID is %d", pm.nick, pm.sequence),
		fmt.Sprintf("I have %d friend(s)", len(pm.friends)),
		fmt.Sprintf("We're meeting at '%s'", pm.venue),
		"\r\nThese are my friends:\r\n",
	}

	for _, f := range pm.friends {
		s = append(s, f.String())
	}

	s = append(s, "\r\nI haven't heard from these guys in a while:\r\n")
	for _, f := range pm.oldfriends {
		s = append(s, f.String())
	}

	return strings.Join(s, "\r\n")
}

func (pm *PeerManager) Publish(dst, cmd, payload string) error {
	pm.mtx.Lock()
	pm.sequence++
	m := &BusMessage{From: pm.nick, To: dst, Seq: pm.sequence, Cmd: cmd, Payload: payload}
	pm.mtx.Unlock()

	return pm.bus.Send(m.Bytes())
}

func (pm *PeerManager) Loop() {
	ih, err := dht.DecodeInfoHash(pm.venue)
	if err != nil {
		log.Printf(`can't decode infohash: %s`, err)
		return
	}

	if err := pm.d.Start(); err != nil {
		log.Printf(`can't start DHT: %s`, err)
		return
	}

	log.Printf(`DHT bound to port %d`, pm.d.Port())

	if err := pm.bus.ListenOptions(fmt.Sprintf("tls+tcp://*:%d", pm.d.Port()), pm.opts); err != nil {
		log.Fatalf(`can't listen on BUS socket: %s`, err)
	}

	go pm.drainPeers()

	go func() {
		for {
			time.Sleep(2 * time.Second)
			pm.mtx.Lock()
			for name, f := range pm.friends {
				f.mtx.Lock()
				if f.lastSeen.Add(10 * time.Second).Before(time.Now()) {
					log.Printf(`haven't heard from %s in a while`, name)
					pm.oldfriends[name] = f
					delete(pm.friends, name)
				}
				f.mtx.Unlock()
			}
			pm.mtx.Unlock()
		}
	}()

	go func() {
		for {
			/* Receive message */
			m, err := NewBusMessage(pm.bus.Recv())
			if err != nil {
				log.Printf(`can't receive message from bus: %s`, err)
				continue
			}

			/* Ignore messages from ourselves */
			if m.From == pm.nick {
				continue
			}

			/* Ignore messages not to 'everyone' or us */
			if m.To != pm.nick && m.To != "*" {
				continue
			}

			pm.mtx.Lock()
			f, ok := pm.friends[m.From]
			if !ok {
				f = NewFriend(m.From, pm)
				pm.friends[m.From] = f
				pm.need_full_sync = true
			}
			pm.mtx.Unlock()

			f.HandleMessage(m)

			if !ok {
				log.Printf(`%s is a new friend!`, m.From)
				pm.Publish(m.From, "hello", "")
			}
		}
	}()

	go func() {
		i := 0
		for {
			pm.Publish("*", "i'm alive", fmt.Sprintf("%d", i))
			i++
			time.Sleep(5 * time.Second)
		}
	}()

	go func() {
		for {
			buf, err := json.Marshal(<-pm.db.Events)
			if err != nil {
				log.Fatalf(`can't marshal event: %s`, err)
			}
			pm.Publish("*", "event", string(buf))
		}
	}()

	go func() {
		for {
			time.Sleep(5 * time.Second)
			pm.mtx.Lock()
			if !pm.need_full_sync {
				pm.mtx.Unlock()
				continue
			}
			pm.need_full_sync = false
			pm.mtx.Unlock()

			log.Println(`doing a full sync`)

			for _, env := range pm.db.AllEnvelopes() {
				_, events, err := pm.db.EnvelopeWithHistory(env.Id)
				if err != nil {
					log.Fatalf(`can't get events for envelope %s: %s`, env.Id, err)
				}
				for _, e := range events {
					pm.db.Events <- e
				}
			}
		}
	}()

	for {
		pm.d.PeersRequest(string(ih), true)
		time.Sleep(5 * time.Second)
	}
}

func (pm *PeerManager) drainPeers() {
	log.Printf(`draining DHT`)
	seen := make(map[string]struct{})

	for r := range pm.d.PeersRequestResults {
		for _, peers := range r {
			for _, x := range peers {
				addr := dht.DecodePeerAddress(x)
				if _, ok := seen[addr]; !ok {
					pm.connectToPeer(addr)
				}
				seen[addr] = struct{}{}
			}
		}
	}
}

func (pm *PeerManager) connectToPeer(addr string) {
	if err := pm.bus.DialOptions(fmt.Sprintf("tls+tcp://%s", addr), pm.opts); err != nil {
		log.Printf(`can't connect SUB to %s: %s`, addr, err)
	}
}
