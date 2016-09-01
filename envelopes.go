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

	id := r.FormValue("id")

	db.DeleteEnvelope(id)

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func handleUpdateRequest(db *DB, w http.ResponseWriter, r *http.Request) {
	log.Printf(`update: %v`, r.URL)

	log.Printf(`name: %s`, r.FormValue("env-name"))
	log.Printf(`target: %s`, r.FormValue("env-target"))
	log.Printf(`monthtarget: %s`, r.FormValue("env-monthtarget"))
	log.Printf(`balance: %s`, r.FormValue("env-balance"))
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

	newBalance := 0
	if bal, err := strconv.ParseFloat(r.FormValue("env-balance"), 64); err == nil {
		newBalance = int(bal * 100)
	}

	newTarget := 0
	if tgt, err := strconv.ParseFloat(r.FormValue("env-target"), 64); err == nil {
		newTarget = int(tgt * 100)
	}

	newMonthTarget := 0
	if monthtgt, err := strconv.ParseFloat(r.FormValue("env-monthtarget"), 64); err == nil {
		newMonthTarget = int(monthtgt * 100)
	}

	if err = db.UpdateEnvelope(id, name, newBalance, newTarget, newMonthTarget); err != nil {
		log.Printf(`can't update envelope %s: %s`, id, err)
		http.Redirect(w, r, returnTo, http.StatusSeeOther)
		return
	}

	http.Redirect(w, r, returnTo, http.StatusSeeOther)
}

func handleDebug(pm *PeerManager, w http.ResponseWriter, r *http.Request) {
	w.Header().Add("Content-Type", "text/plain")
	w.Write([]byte("Peer manager stats:\r\n\r\n"))
	w.Write([]byte(pm.String()))
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

	param := struct {
		Envelope *Envelope
		Events   []Event
	}{e, events}

	if err := templ.ExecuteTemplate(w, "details.html", param); err != nil {
		log.Printf(`error rendering details template: %s`, err)
	}
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

	pm := NewPeerManager(db)
	go pm.Loop()

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
	http.HandleFunc("/debug", func(w http.ResponseWriter, r *http.Request) {
		handleDebug(pm, w, r)
	})
	err = http.ListenAndServe("127.0.0.1:8081", nil)
	if err != nil {
		log.Printf(`HTTP died: %s`, err)
	}
}
