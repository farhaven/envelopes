package main

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"sync"
	"html/template"

	"github.com/boltdb/bolt"
)

var templFuncs = template.FuncMap{
	"prettyDisplay": prettyDisplay,
	"delta": computeDelta,
}
var templ = template.Must(template.New("").Funcs(templFuncs).ParseGlob("templates/*.html"))

type Envelope struct {
	// Values in Euro-cents
	Balance int
	Target  int
	Name    string
	m       sync.Mutex
}

func (e *Envelope) String() string {
	return fmt.Sprintf("<Envelope '%s', Balance: %d, Target: %d>", e.Name, e.Balance, e.Target)
}

func EnvelopeFromDB(db *bolt.DB, name string) *Envelope {
	e := &Envelope{Name: name}

	err := db.View(func(tx *bolt.Tx) error {
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

		e.Balance = bal
		e.Target = tgt

		return nil
	})

	if err != nil {
		log.Printf("%s", err)
	}

	return e
}

func (e *Envelope) Persist(db *bolt.DB) error {
	err := db.Batch(func(tx *bolt.Tx) error {
		e.m.Lock()
		defer e.m.Unlock()

		envelopes, err := tx.CreateBucketIfNotExists([]byte("envelopes"))
		if err != nil {
			return err
		}

		eb, err := envelopes.CreateBucketIfNotExists([]byte(e.Name))
		if err != nil {
			return err
		}

		bal := []byte(strconv.Itoa(e.Balance))
		if err = eb.Put([]byte("balance"), bal); err != nil {
			return err
		}

		tgt := []byte(strconv.Itoa(e.Target))
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

	e.Balance += delta
}

func AllEnvelopes(db *bolt.DB) []*Envelope {
	rv := []*Envelope{}

	err := db.View(func (tx *bolt.Tx) error {
		envelopes := tx.Bucket([]byte("envelopes"))
		if envelopes == nil {
			return errors.New(`can't find bucket 'envelopes'`)
		}

		c := envelopes.Cursor()
		for k, _ := c.First(); k != nil; k, _ = c.Next() {
			rv = append(rv, EnvelopeFromDB(db, string(k)))
		}

		return nil
	})

	if err != nil {
		log.Printf("%s", err)
	}

	return rv
}

func prettyDisplay(cents int) string {
	return fmt.Sprintf("%.02f", float64(cents) / 100)
}

func computeDelta(balance, target int) []string {
	delta := balance - target
	cls := "delta-ok"
	if delta < 0 {
		cls = "delta-warn"
	}
	return []string{cls, fmt.Sprintf(`%.02f`, float64(delta) / 100)}
}

func handleDeleteRequest(db *bolt.DB, w http.ResponseWriter, r *http.Request) {
	log.Printf(`delete: %v`, r.URL)
	log.Printf(`name: %s`, r.FormValue("name"))
	if err := db.Update(func (tx *bolt.Tx) error {
		envelopes := tx.Bucket([]byte("envelopes"))
		if envelopes == nil {
			return errors.New(`can't find bucket 'envelopes'`)
		}

		return envelopes.DeleteBucket([]byte(r.FormValue("name")))
	}); err != nil {
		log.Printf(`err: %s`, err)
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func handleUpdateRequest(db *bolt.DB, w http.ResponseWriter, r *http.Request) {
	log.Printf(`update: %v`, r.URL)

	log.Printf(`name: %s`, r.FormValue("env-name"))
	log.Printf(`newname: %s`, r.FormValue("env-new-name"))
	log.Printf(`target: %s`, r.FormValue("env-target"))
	log.Printf(`balance: %s`, r.FormValue("env-balance"))

	name := r.FormValue("env-name")
	newname := r.FormValue("env-new-name")

	env := EnvelopeFromDB(db, name)
	if newname != "" && newname != name {
		err := db.Update(func (tx *bolt.Tx) error {
			envelopes := tx.Bucket([]byte("envelopes"))
			if envelopes == nil {
				return errors.New(`can't find bucket 'envelopes'`)
			}

			return envelopes.DeleteBucket([]byte(name))
		})
		if err == nil {
			env.Name = newname
		}
	}

	tgt, err := strconv.ParseFloat(r.FormValue("env-target"), 64)
	if err != nil {
		log.Printf(`err: %s`, err)
	} else {
		env.Target = int(tgt * 100)
	}

	bal, err := strconv.ParseFloat(r.FormValue("env-balance"), 64)
	if err != nil {
		log.Printf(`err: %s`, err)
	} else {
		env.Balance = int(bal * 100)
	}

	env.Persist(db)

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func handleRequest(db *bolt.DB, w http.ResponseWriter, r *http.Request) {
	log.Printf(`request: %v`, r.URL)

	w.Header().Add("Content-Type", "text/html")
	es := AllEnvelopes(db)
	d := int(0)
	for i := range es {
		d += es[i].Balance - es[i].Target
	}
	dcls := "delta-ok"
	if d < 0 {
		dcls = "delta-warn"
	}
	param := struct {
		Envelopes []*Envelope
		TotalDelta struct{
			Cls string
			Val int
		}
	}{
		Envelopes: es,
		TotalDelta: struct{Cls string; Val int}{ dcls, d },
	}
	templ.ExecuteTemplate(w, "index.html", param)
}

func main() {
	log.Printf("Here we go")

	db, err := bolt.Open("envelopes.db", 0600, nil)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	http.Handle("/static/", http.FileServer(http.Dir(".")))
	http.HandleFunc("/", func (w http.ResponseWriter, r *http.Request) {
		handleRequest(db, w, r)
	})
	http.HandleFunc("/update", func (w http.ResponseWriter, r *http.Request) {
		handleUpdateRequest(db, w, r)
	})
	http.HandleFunc("/delete", func (w http.ResponseWriter, r *http.Request) {
		handleDeleteRequest(db, w, r)
	})
	log.Fatal(http.ListenAndServe("127.0.0.1:8081", nil))
}
