package main

import (
	"crypto/rand"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/nictuku/dht"
	nn "github.com/op/go-nanomsg"
)

type BusMessage struct {
	From    string
	To      string
	Payload []string
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
}

func NewFriend(name string) *Friend {
	return &Friend{name: name, mtx: &sync.Mutex{}, lastSeen: time.Now()}
}

func (f *Friend) String() string {
	f.mtx.Lock()
	defer f.mtx.Unlock()

	return fmt.Sprintf("Name: %s, last message: %s, last seen: %s", f.name, f.msg.Payload, f.lastSeen)
}

func (f *Friend) HandleMessage(msg *BusMessage) {
	f.mtx.Lock()
	defer f.mtx.Unlock()

	f.msg = msg
	f.lastSeen = time.Now()
}

type PeerManager struct {
	d          *dht.DHT
	bus        *nn.BusSocket
	nick       string
	friends    map[string]*Friend
	oldfriends map[string]*Friend
	venue      string
	mtx        *sync.RWMutex
}

func NewPeerManager() *PeerManager {
	conf := dht.NewConfig()
	conf.Port = 55000
	conf.CleanupPeriod = 3 * time.Second

	pm := PeerManager{}

	var err error
	pm.bus, err = nn.NewBusSocket()
	if err != nil {
		log.Fatalf(`can't create BUS socket: %s`, err)
	}

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

func (pm *PeerManager) String() string {
	pm.mtx.RLock()
	defer pm.mtx.RUnlock()

	s := []string{
		fmt.Sprintf("DHT: Port: %d", pm.d.Port()),
		fmt.Sprintf("\r\nI am %s", pm.nick),
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

func (pm *PeerManager) Publish(dst string, payload []string) error {
	m := &BusMessage{From: pm.nick, To: dst, Payload: payload}
	_, err := pm.bus.Send(m.Bytes(), 0)
	return err
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

	ep, err := pm.bus.Bind(fmt.Sprintf("tcp://*:%d", pm.d.Port()))
	if err != nil {
		log.Fatalf(`can't bind BUS socket: %s`, err)
	}
	log.Printf(`BUS endpoint: %s`, ep)

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
			m, err := NewBusMessage(pm.bus.Recv(0))
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

			log.Printf(`tgt: %s, src: %s, msg: %v`, m.To, m.From, m.Payload)

			pm.mtx.Lock()
			f, ok := pm.friends[m.From]
			if !ok {
				f = NewFriend(m.From)
				pm.friends[m.From] = f
			}
			pm.mtx.Unlock()

			f.HandleMessage(m)

			if !ok {
				log.Printf(`%s is a new friend!`, m.From)
				pm.Publish(m.From, []string{"hello friend :)"})
			}
		}
	}()

	go func() {
		i := 0
		for {
			pm.Publish("*", []string{"I'm alive", fmt.Sprintf("%d", i)})
			i++
			time.Sleep(5 * time.Second)
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
					pm.connectToPeer(a)
				}
				seen[addr] = struct{}{}
			}
		}
	}
}

func (pm *PeerManager) connectToPeer(addr string) {
	if _, err := pm.bus.Connect(fmt.Sprintf("tcp://%s", addr)); err != nil {
		log.Printf(`can't connect SUB to %s: %s`, addr, err)
	}
}
