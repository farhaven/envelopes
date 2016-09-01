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
	Seq int64
	Cmd string
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
	pm *PeerManager
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

	log.Printf(`tgt: %s, src: %s, cmd: %s, payload: %v`, m.To, m.From, m.Cmd, m.Payload)

	if m.Cmd == "event" {
		ev := Event{}
		if err := json.Unmarshal([]byte(m.Payload), &ev); err != nil {
			log.Printf(`can't unmarshal event payload: %s`, err)
		}
		log.Printf(`got an event: %v`, ev)

		if err := f.pm.db.MergeEvent(ev); err != nil {
			log.Printf(`can't merge event: %s`, err)
		}
	}
}

type PeerManager struct {
	d          *dht.DHT
	db *DB
	bus        *nn.BusSocket
	nick       string
	friends    map[string]*Friend
	oldfriends map[string]*Friend
	venue      string
	sequence   int64
	mtx        *sync.RWMutex
}

func NewPeerManager(db *DB) *PeerManager {
	conf := dht.NewConfig()
	conf.Port = 55000
	conf.CleanupPeriod = 3 * time.Second

	pm := PeerManager{db: db}

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

			pm.mtx.Lock()
			f, ok := pm.friends[m.From]
			if !ok {
				f = NewFriend(m.From, pm)
				pm.friends[m.From] = f
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
	if _, err := pm.bus.Connect(fmt.Sprintf("tcp://%s", addr)); err != nil {
		log.Printf(`can't connect SUB to %s: %s`, addr, err)
	}
}
