package main

import (
	"log"
	"fmt"
	"errors"
	"encoding/binary"
	"github.com/boltdb/bolt"
)

type Envelope struct {
	// Values in Euro-cents
	balance int64
	target int64
	name string
}

func EnvelopeFromDB(db *bolt.DB, name string) Envelope {
	e := Envelope{name: name}

	err := db.View(func (tx *bolt.Tx) error {
		envelopes := tx.Bucket([]byte("envelopes"))
		if envelopes == nil {
			return errors.New(`can't find bucket "envelopes"`)
		}

		eb := envelopes.Bucket([]byte(name))
		if eb == nil {
			return errors.New(fmt.Sprintf(`can't find envelope %s`, name))
		}

		bal_s := eb.Get([]byte("balance"))
		tgt_s := eb.Get([]byte("target"))

		if bal_s == nil || tgt_s == nil {
			return errors.New(fmt.Sprintf(`can't find balance or target for envelope %s`, name))
		}

		bal, n := binary.Varint(bal_s)
		if bal == 0 && n <= 0 {
			return errors.New(fmt.Sprintf(`balance value for envelope %s is corrupted: %s`, name, bal_s))
		}

		tgt, n := binary.Varint(tgt_s)
		if tgt == 0 && n <= 0 {
			return errors.New(fmt.Sprintf(`target value for envelope %s is corrupted: %s`, name, tgt_s))
		}

		e.balance = bal
		e.target = tgt

		return nil
	})

	if err != nil {
		log.Printf("%s", err)
	}

	return e
}

func (e *Envelope) Persist(db *bolt.DB) error {
	err := db.Batch(func (tx *bolt.Tx) error {
		envelopes, err := tx.CreateBucketIfNotExists([]byte("envelopes"))
		if err != nil {
			return err
		}

		eb, err := envelopes.CreateBucketIfNotExists([]byte(e.name))
		if err != nil {
			return err
		}

		bal := make([]byte, binary.MaxVarintLen64)
		bal_n := binary.PutVarint(bal, e.balance)

		if err = eb.Put([]byte("target"), bal[:bal_n]); err != nil {
			return err
		}

		tgt := make([]byte, binary.MaxVarintLen64)
		tgt_n := binary.PutVarint(tgt, e.target)

		if err = eb.Put([]byte("balance"), tgt[:tgt_n]); err != nil {
			return err
		}

		return nil
	})

	return err
}

func main() {
	log.Printf("Here we go")

	db, err := bolt.Open("envelopes.db", 0600, nil)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	e := EnvelopeFromDB(db, "Miete")
	log.Printf("%v", e)

	e.balance = 100
	e.target = 0

	if err := e.Persist(db); err != nil {
		log.Fatal(err)
	}

	log.Printf("done")
}
