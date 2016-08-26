package main

import (
	"fmt"
	"log"
	"net"
	"time"
	"github.com/nictuku/dht"
)

func dhtLoop() {
	ih, err := dht.DecodeInfoHash("7e16e0dd7b96e70363c3bcffc6207460d14fe3da")
	if err != nil {
		log.Printf(`can't decode infohash: %s`, err)
		return
	}

	d, err := dht.New(nil)
	if err != nil {
		log.Printf(`can't create DHT: %s`, err)
		return
	}

	if err = d.Start(); err != nil {
		log.Printf(`can't start DHT: %s`, err)
		return
	}

	go drainPeers(d)
	go handleIncoming(d)

	for {
		d.PeersRequest(string(ih), true)
		time.Sleep(5 * time.Second)
	}
}

func handleIncoming(d *dht.DHT) {
	log.Printf(`DHT bound to port %d`, d.Port())

	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", d.Port()))
	if err != nil {
		log.Printf(`can't open listening socket: %s`, err)
		return
	}

	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Printf(`accept() failed: %s`, err)
			continue
		}

		log.Printf(`incoming connection from %s`, conn.RemoteAddr())
		conn.Close()
	}
}

func talkToPeer(addr string) {
	log.Printf(`got a new peer: %s`, addr)

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		log.Printf(`can't dial %s: %s`, addr, err)
		return
	}
	defer conn.Close()

	log.Printf(`connected to %s, local addr: %s`, addr, conn.LocalAddr())
}

func drainPeers(d *dht.DHT) {
	log.Printf(`draining DHT`)
	seen := make(map[string]struct{})

	for r := range d.PeersRequestResults {
		for _, peers := range r {
			for _, x := range peers {
				addr := dht.DecodePeerAddress(x)
				if _, ok := seen[addr]; !ok {
					go talkToPeer(addr)
				}
				seen[addr] = struct{}{}
			}
		}
	}
}
