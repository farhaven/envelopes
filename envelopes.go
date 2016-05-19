package main

import (
	"log"
	"fmt"
	"errors"
	"strconv"
	"github.com/boltdb/bolt"
	"sync"
)

type Envelope struct {
	// Values in Euro-cents
	balance int
	target int
	name string
	m sync.Mutex
}

func (e *Envelope) String() string {
	return fmt.Sprintf("<Envelope '%s', Balance: %d, Target: %d>", e.name, e.balance, e.target)
}

func EnvelopeFromDB(db *bolt.DB, name string) *Envelope {
	e := &Envelope{name: name}

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

		bal, err := strconv.Atoi(string(bal_s))
		if err != nil {
			return errors.New(fmt.Sprintf(`balance value for envelope %s is corrupted: %s: %s`, name, bal_s, err))
		}

		tgt, err := strconv.Atoi(string(tgt_s))
		if err != nil {
			return errors.New(fmt.Sprintf(`target value for envelope %s is corrupted: %s: %s`, name, tgt_s, err))
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
		e.m.Lock()
		defer e.m.Unlock()

		envelopes, err := tx.CreateBucketIfNotExists([]byte("envelopes"))
		if err != nil {
			return err
		}

		eb, err := envelopes.CreateBucketIfNotExists([]byte(e.name))
		if err != nil {
			return err
		}

		bal := []byte(strconv.Itoa(e.balance))
		if err = eb.Put([]byte("balance"), bal); err != nil {
			return err
		}

		tgt := []byte(strconv.Itoa(e.target))
		if err = eb.Put([]byte("target"), tgt); err != nil {
			return err
		}

		return nil
	})

	return err
}

func (e *Envelope) IncBalance(delta int) {
	e.m.Lock()
	defer e.m.Unlock()

	e.balance += delta
}

func main() {
	log.Printf("Here we go")

	db, err := bolt.Open("envelopes.db", 0600, nil)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	e := EnvelopeFromDB(db, "Miete")
	log.Printf("From DB: %v", e)

	e.IncBalance(10)

	log.Printf("To DB: %v", e)

	if err := e.Persist(db); err != nil {
		log.Fatal(err)
	}

	log.Printf("done")
}
