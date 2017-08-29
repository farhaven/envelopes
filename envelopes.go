package main

import (
	"fmt"
	"html/template"
	"log"
	"net/http"
	"strconv"

	"github.com/google/uuid"
)

var templFuncs = template.FuncMap{
	"prettyDisplay": prettyDisplay,
	"delta":         computeDelta,
}
var templ = template.Must(template.New("").Funcs(templFuncs).ParseGlob("templates/*.html"))

func prettyDisplay(cents int) string {
	return fmt.Sprintf("%.02f", float64(cents)/100)
}

func computeDelta(balance, target int) []string {
	delta := balance - target
	cls := "delta-ok"
	if delta < 0 {
		cls = "delta-warn"
	}
	return []string{cls, fmt.Sprintf(`%.02f`, float64(delta)/100)}
}

func handleDeleteRequest(db *DB, w http.ResponseWriter, r *http.Request) {
	log.Printf(`delete: %v`, r.URL)
	log.Printf(`id: %s`, r.FormValue("id"))

	id, err := uuid.Parse(r.FormValue("id"))
	if err != nil {
		log.Printf(`update: can't parse ID: %s`, err)
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	db.DeleteEnvelope(id)

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func handleUpdateRequest(db *DB, w http.ResponseWriter, r *http.Request) {
	log.Printf(`update: %v`, r.URL)

	log.Printf(`name: %s`, r.FormValue("env-name"))
	log.Printf(`target: %s`, r.FormValue("env-target"))
	log.Printf(`monthtarget: %s`, r.FormValue("env-monthtarget"))
	log.Printf(`return: %s`, r.FormValue("env-return"))

	returnTo := "/"
	if r.FormValue("env-return") != "" {
		returnTo = "/details?id=" + r.FormValue("env-return")
	}

	id, err := uuid.Parse(r.FormValue("env-id"))
	if err != nil {
		log.Printf(`update: can't parse ID: %s`, err)
		id = uuid.New()
	}

	name := r.FormValue("env-name")

	newTarget := 0
	if tgt, err := strconv.ParseFloat(r.FormValue("env-target"), 64); err == nil {
		newTarget = int(tgt * 100)
	}

	newMonthTarget := 0
	if monthtgt, err := strconv.ParseFloat(r.FormValue("env-monthtarget"), 64); err == nil {
		newMonthTarget = int(monthtgt * 100)
	}

	if err = db.UpdateEnvelopeMeta(id, name, newTarget, newMonthTarget); err != nil {
		log.Printf(`can't update envelope %s: %s`, id, err)
		http.Redirect(w, r, returnTo, http.StatusSeeOther)
		return
	}

	http.Redirect(w, r, returnTo, http.StatusSeeOther)
}

func handleDebug(w http.ResponseWriter, r *http.Request) {
	w.Header().Add("Content-Type", "text/plain")
	w.Write([]byte("Hic sunt dracones\r\n\r\n"))
}

func handleDetail(db *DB, w http.ResponseWriter, r *http.Request) {
	log.Printf(`handling detail for id %s`, r.FormValue("id"))
	id, err := uuid.Parse(r.FormValue("id"))
	if err != nil {
		log.Printf(`detail: can't parse ID: %s`, err)
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	e, events, err := db.EnvelopeWithHistory(id)
	if err != nil {
		log.Printf(`detail: can't get envelope and history from DB: %s`, err)
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	events_rev := []Event{}
	for idx := len(events) - 1; idx >= 0; idx-- {
		events_rev = append(events_rev, events[idx])
	}

	param := struct {
		Envelope *Envelope
		Events   []Event
	}{e, events_rev}

	if err := templ.ExecuteTemplate(w, "details.html", param); err != nil {
		log.Printf(`error rendering details template: %s`, err)
	}
}

func handleTx(db *DB, w http.ResponseWriter, r *http.Request) {
	log.Printf(`tx for id %s`, r.FormValue(`id`))

	id, err := uuid.Parse(r.FormValue("id"))
	if err != nil {
		log.Printf(`tx: can't parse ID: %s`, err)
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	dir := r.FormValue(`dir`)
	switch dir {
	case `in`:
		/* nothing */
	case `out`:
		/* nothing */
	case `inout`:
		/* nothing */
	default:
		dir = `inout`
	}

	env, err := db.Envelope(id)
	if err != nil {
		log.Printf(`tx: can't get envelope %s: %s`, r.FormValue(`id`), err)
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	if r.Method != "POST" {
		switch dir {
		case `in`:
			fallthrough
		case `out`:
			log.Printf(`tx %s`, dir)
			params := struct {
				Envelope  *Envelope
				Direction string
			}{
				Envelope:  env,
				Direction: dir,
			}
			if err := templ.ExecuteTemplate(w, "transfer_in.html", params); err != nil {
				log.Printf(`error rendering details template: %s`, err)
			}
		default:
			params := struct {
				AllEnvelopes []*Envelope
				This         *Envelope
			}{
				AllEnvelopes: []*Envelope{},
				This:         env,
			}
			for _, e := range db.AllEnvelopes() {
				if e.Id != env.Id {
					params.AllEnvelopes = append(params.AllEnvelopes, e)
				}
			}
			if err := templ.ExecuteTemplate(w, "transfer.html", params); err != nil {
				log.Printf(`error rendering details template: %s`, err)
			}
			log.Printf(`tx inout`)
		}
	} else {
		log.Printf(`updating env %s`, r.FormValue(`id`))
		log.Printf(`  amount: %s`, r.FormValue(`amount`))
		amount, err := strconv.ParseFloat(r.FormValue(`amount`), 64)
		if err != nil {
			log.Printf(`can't parse %s: %s`, r.FormValue(`amount`), err)
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		switch dir {
		case `in`:
			if err = db.UpdateEnvelopeBalance(id, int(amount*100)); err != nil {
				log.Printf(`can't update balance: %s`, err)
			}
		case `out`:
			if err = db.UpdateEnvelopeBalance(id, -int(amount*100)); err != nil {
				log.Printf(`can't update balance: %s`, err)
			}
		default:
			destId, err := uuid.Parse(r.FormValue("destination"))
			if err != nil {
				log.Printf(`tx: can't parse ID: %s`, err)
				http.Redirect(w, r, "/", http.StatusSeeOther)
				return
			}
			if err = db.UpdateEnvelopeBalance(destId, int(amount*100)); err != nil {
				log.Printf(`can't update balance: %s`, err)
			} else if err = db.UpdateEnvelopeBalance(id, -int(amount*100)); err != nil {
				log.Printf(`can't update balance: %s`, err)
			}
			http.Redirect(w, r, fmt.Sprintf("/details?id=%s", destId), http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/details?id=%s", r.FormValue(`id`)), http.StatusSeeOther)
		return
	}
}

func handleSpread(db *DB, w http.ResponseWriter, r *http.Request) {
	log.Printf(`handling spread for id %s`, r.FormValue("id"))

	id, err := uuid.Parse(r.FormValue("id"))
	if err != nil {
		log.Printf(`spread: can't parse ID: %s`, err)
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	if err := db.Spread(id); err != nil {
		log.Printf(`something went wrong with the spread`, err)
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func handleRequest(db *DB, w http.ResponseWriter, r *http.Request) {
	if r.URL.String() != "/" {
		log.Printf(`ignoring request for %s`, r.URL)
		http.NotFound(w, r)
		return
	}

	log.Printf(`request: %v`, r.URL)

	w.Header().Add("Content-Type", "text/html")
	es := db.AllEnvelopes()
	delta := int(0)
	balance := int(0)
	monthtarget := int(0)
	for i := range es {
		delta += es[i].Balance - es[i].Target
		balance += es[i].Balance
		monthtarget += es[i].MonthTarget
	}
	dcls := "delta-ok"
	if delta < 0 {
		dcls = "delta-warn"
	}
	param := struct {
		Envelopes  []*Envelope
		TotalDelta struct {
			Cls string
			Val int
		}
		TotalBalance int
		MonthTarget  int
	}{
		es,
		struct {
			Cls string
			Val int
		}{dcls, delta},
		balance,
		monthtarget,
	}

	if err := templ.ExecuteTemplate(w, "index.html", param); err != nil {
		log.Printf(`error rendering overview template: %s`, err)
	}
}

func main() {
	log.Printf("Here we go")

	db, err := OpenDB()
	if err != nil {
		log.Fatal(err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			log.Printf(`error while saving DB: %s`, err)
		}
	}()

	http.Handle("/static/", http.FileServer(http.Dir(".")))
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		handleRequest(db, w, r)
	})
	http.HandleFunc("/update", func(w http.ResponseWriter, r *http.Request) {
		handleUpdateRequest(db, w, r)
	})
	http.HandleFunc("/delete", func(w http.ResponseWriter, r *http.Request) {
		handleDeleteRequest(db, w, r)
	})
	http.HandleFunc("/details", func(w http.ResponseWriter, r *http.Request) {
		handleDetail(db, w, r)
	})
	http.HandleFunc("/spread", func(w http.ResponseWriter, r *http.Request) {
		handleSpread(db, w, r)
	})
	http.HandleFunc("/tx", func(w http.ResponseWriter, r *http.Request) {
		handleTx(db, w, r)
	})
	http.HandleFunc("/debug", func(w http.ResponseWriter, r *http.Request) {
		handleDebug(w, r)
	})
	err = http.ListenAndServe("127.0.0.1:8081", nil)
	if err != nil {
		log.Printf(`HTTP died: %s`, err)
	}
}
