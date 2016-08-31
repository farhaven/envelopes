package main

import (
	"crypto/rand"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net"
	"time"

	"github.com/nictuku/dht"
	zmq "github.com/pebbe/zmq4"
)

type PeerManager struct {
	d        *dht.DHT
	sub      *zmq.Socket
	pub      *zmq.Socket
	pubchan  chan []string
	addrchan chan interface{}
	r        *zmq.Reactor
	nick     string
	friends  map[string]bool
}

func NewPeerManager() PeerManager {
	conf := dht.NewConfig()
	conf.Port = 55000

	pm := PeerManager{}

	var err error
	if pm.pub, err = zmq.NewSocket(zmq.PUB); err != nil {
		log.Fatalf(`can't create PUB socket: %s`, err)
	}
	if pm.sub, err = zmq.NewSocket(zmq.SUB); err != nil {
		log.Fatalf(`can't create SUB socket: %s`, err)
	}

	pm.sub.SetSubscribe("*")
	pm.sub.SetSubscribe(pm.nick)

	pm.pubchan = make(chan []string)
	pm.addrchan = make(chan interface{}, 20)

	if pm.d, err = dht.New(conf); err != nil {
		log.Fatalf(`can't create DHT: %s`, err)
	}

	buf := make([]byte, 4)
	rand.Read(buf)
	pm.nick = hex.EncodeToString(buf)
	log.Printf(`my nickname is %s`, pm.nick)

	pm.friends = make(map[string]bool)

	return pm
}

func (pm *PeerManager) Loop() {
	/* XXX: Autogenerate? */
	sum := sha1.Sum([]byte("LetsMeetHere"))

	log.Printf(`I will meet my friends at %s`, hex.EncodeToString(sum[:]))

	ih, err := dht.DecodeInfoHash(hex.EncodeToString(sum[:]))
	if err != nil {
		log.Printf(`can't decode infohash: %s`, err)
		return
	}

	if err := pm.d.Start(); err != nil {
		log.Printf(`can't start DHT: %s`, err)
		return
	}

	log.Printf(`DHT bound to port %d`, pm.d.Port())

	if err := pm.pub.Bind(fmt.Sprintf("tcp://*:%d", pm.d.Port())); err != nil {
		log.Fatalf(`can't bind PUB socket: %s`, err)
	}
	if err := pm.sub.Bind(fmt.Sprintf("tcp://*:%d", pm.d.Port()+1)); err != nil {
		log.Fatalf(`can't bind PUB socket: %s`, err)
	}

	go pm.drainPeers()
	/* These needs to be synchroneous because 0MQ sockets are not threadsafe */
	go func() {
		for {
			pm.pub.SendMessageDontwait(<-pm.pubchan)
		}
	}()
	go func() {
		r := zmq.NewReactor()
		r.AddSocket(pm.sub, zmq.POLLIN, func(s zmq.State) error {
			/* Handle socket input here */
			msg, err := pm.sub.RecvMessage(0)
			if err != nil {
				return err
			}

			if len(msg) < 2 {
				return errors.New(`short message received`)
			}

			tgt := msg[0]
			src := msg[1]

			if src == pm.nick {
				return nil
			}

			log.Printf(`tgt: %s, src: %s, msg: %v`, tgt, src, msg[2:])

			if _, ok := pm.friends[src]; !ok {
				log.Printf(`%s is a new friend!`, src)
				pm.friends[src] = true
				pm.pubchan <- []string{src, pm.nick, "hello friend :)"}
			}

			return nil
		})
		r.AddChannel(pm.addrchan, 1, func(a interface{}) error {
			addr := a.(string)
			pm.connectToPeer(addr)
			return nil
		})

		for {
			if err := r.Run(1 * time.Second); err != nil {
				log.Fatalf(`can't run reactor: %s`, err)
			}
			log.Printf(`polling for messages`)
		}
	}()

	go func() {
		/* This is just for testing */
		i := 0
		for {
			pm.pubchan <- []string{"*", pm.nick, fmt.Sprintf("%d", i)}
			i++
			time.Sleep(1 * time.Second)
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
					pm.addrchan <- addr
				}
				seen[addr] = struct{}{}
			}
		}
	}
}

func (pm *PeerManager) connectToPeer(ip string) {
	addr, err := net.ResolveTCPAddr("tcp", ip)
	if err != nil {
		log.Fatalf(`can't parse tcp address %s: %s`, addr, err)
	}

	if err := pm.sub.Connect(fmt.Sprintf("tcp://%s:%d", addr.IP, addr.Port)); err != nil {
		log.Fatalf(`can't connect SUB to %s: %s`, addr, err)
	}
	if err := pm.pub.Connect(fmt.Sprintf("tcp://%s:%d", addr.IP, addr.Port+1)); err != nil {
		log.Fatalf(`can't connect SUB to %s:%d: %s`, addr.IP, addr.Port, err)
	}
}
