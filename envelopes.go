package main

import (
	"fmt"
	"log"
	"net/http"
	"strconv"
	"sync"
	"html/template"

	_ "github.com/mattn/go-sqlite3"
	"database/sql"
)

var templFuncs = template.FuncMap{
	"prettyDisplay": prettyDisplay,
	"delta": computeDelta,
}
var templ = template.Must(template.New("").Funcs(templFuncs).ParseGlob("templates/*.html"))

type Envelope struct {
	// Values in Euro-cents
	Id int
	Balance int
	Target  int
	Name    string
	m       sync.Mutex
}

func (e *Envelope) String() string {
	return fmt.Sprintf("<Envelope '%s', Balance: %d, Target: %d>", e.Name, e.Balance, e.Target)
}

func EnvelopeFromDB(tx *sql.Tx, name string) *Envelope {
	e := Envelope{Name: name}

	err := tx.QueryRow("SELECT id, balance, target FROM envelopes WHERE name = $1", name).Scan(&e.Id, &e.Balance, &e.Target)
	if err == nil {
		return &e
	}
	if err, _ := tx.Exec("INSERT INTO envelopes VALUES (NULL, $1, 0, 0)", name); err != nil {
		log.Printf(`db insert failed: %v`, err)
	}
	return &e
}

func (e *Envelope) IncBalance(delta int) {
	e.m.Lock()
	defer e.m.Unlock()

	e.Balance += delta
}

func allEnvelopes(db *sql.DB) []*Envelope {
	rv := []*Envelope{}

	rows, err := db.Query("SELECT id, name, balance, target FROM envelopes")
	if err != nil {
		log.Printf(`error querying DB: %v`, err)
		return nil
	}
	defer rows.Close()

	for rows.Next() {
		var e Envelope
		if err := rows.Scan(&e.Id, &e.Name, &e.Balance, &e.Target); err != nil {
			log.Printf(`error querying DB: %v`, err)
			return nil
		}
		rv = append(rv, &e)
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

func handleDeleteRequest(db *sql.DB, w http.ResponseWriter, r *http.Request) {
	log.Printf(`delete: %v`, r.URL)
	log.Printf(`name: %s`, r.FormValue("name"))

	_, err := db.Exec("DELETE FROM envelopes WHERE name = $1", r.FormValue("name"))
	if err != nil {
		log.Printf(`err: %s`, err)
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func handleUpdateRequest(db *sql.DB, w http.ResponseWriter, r *http.Request) {
	log.Printf(`update: %v`, r.URL)

	log.Printf(`name: %s`, r.FormValue("env-name"))
	log.Printf(`newname: %s`, r.FormValue("env-new-name"))
	log.Printf(`target: %s`, r.FormValue("env-target"))
	log.Printf(`balance: %s`, r.FormValue("env-balance"))

	name := r.FormValue("env-name")
	newname := r.FormValue("env-new-name")

	tx, err := db.Begin()
	env := EnvelopeFromDB(tx, name)

	if newname != "" {
		env.Name = newname
	}

	deltaBalance := 0
	bal, err := strconv.ParseFloat(r.FormValue("env-balance"), 64)
	if err != nil {
		log.Printf(`err: %s`, err)
	} else {
		deltaBalance = int(bal * 100) - env.Balance
		env.Balance += deltaBalance
	}

	deltaTarget := 0
	tgt, err := strconv.ParseFloat(r.FormValue("env-target"), 64)
	if err != nil {
		log.Printf(`err: %s`, err)
	} else {
		deltaTarget = int(tgt * 100) - env.Target
		env.Target += deltaTarget
	}

	_, err = tx.Exec("INSERT INTO history VALUES (NULL, $1, $2, $3, $4)", env.Id, env.Name, deltaBalance, deltaTarget)
	if err != nil {
		log.Printf(`can't create history entry for change to envelope %d`, env.Id)
	}
	_, err = tx.Exec("UPDATE envelopes SET name = $1, balance = $2, target = $3 WHERE id = $4", env.Name, env.Balance, env.Target, env.Id)
	if err != nil {
		log.Printf(`can't update envelope: %v`, err)
	}
	if err = tx.Commit(); err != nil {
		log.Printf(`can't commit transaction: %v`, err)
	}

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func handleHistory(db *sql.DB, w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.FormValue("id"))
	if err != nil {
		log.Printf(`can't parse ID %s: %s`, r.FormValue("id"), err)
	}

	type Event struct {
		Date string
		Name string
		Balance int
		Target int
	}

	param := struct {
		Id int
		Name string
		Events []Event
	}{
		Id: id,
	}

	tx, err := db.Begin()
	if err != nil {
		log.Printf(`tx: %s`, err)
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	defer tx.Rollback()

	if err := tx.QueryRow("SELECT name FROM envelopes WHERE id = $1", id).Scan(&param.Name); err != nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	rows, err := tx.Query("SELECT id, name, balance, target FROM history WHERE envelope = $1", id)
	if err != nil {
		log.Printf(`can't query history for envelope %d: %s`, id, err)
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	defer rows.Close()

	for rows.Next() {
		var e Event
		var eventId int
		if err := rows.Scan(&eventId, &e.Name, &e.Balance, &e.Target); err != nil {
			log.Printf(`can't scan event %d: %s`, eventId, err)
		}
		param.Events = append(param.Events, e)
	}

	templ.ExecuteTemplate(w, "history.html", param)
}

func handleRequest(db *sql.DB, w http.ResponseWriter, r *http.Request) {
	log.Printf(`request: %v`, r.URL)

	w.Header().Add("Content-Type", "text/html")
	es := allEnvelopes(db)
	delta := int(0)
	balance := int(0)
	for i := range es {
		delta += es[i].Balance - es[i].Target
		balance += es[i].Balance
	}
	dcls := "delta-ok"
	if delta < 0 {
		dcls = "delta-warn"
	}
	param := struct {
		Envelopes []*Envelope
		TotalDelta struct{
			Cls string
			Val int
		}
		TotalBalance int
	}{
		Envelopes: es,
		TotalDelta: struct{Cls string; Val int}{ dcls, delta },
		TotalBalance: balance,
	}
	templ.ExecuteTemplate(w, "index.html", param)
}

func setupDB(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	if _, err := tx.Exec("CREATE TABLE IF NOT EXISTS envelopes (id INTEGER PRIMARY KEY AUTOINCREMENT, name STRING, balance INTEGER, target INTEGER)"); err != nil {
		return err
	}
	if _, err := tx.Exec("CREATE TABLE IF NOT EXISTS history (id INTEGER PRIMARY KEY AUTOINCREMENT, envelope INTEGER, name STRING, balance INTEGER, target INTEGER, FOREIGN KEY(envelope) REFERENCES envelopes(id))"); err != nil {
		return err
	}

	return tx.Commit()
}

func main() {
	log.Printf("Here we go")

	db, err := sql.Open("sqlite3", "envelopes.sqlite")
	if err != nil {
		log.Fatal(err)
	}
	defer func() {
		log.Printf("db stats: %v", db.Stats())
		if err := db.Close(); err != nil {
			log.Printf(`error while saving DB: %s`, err)
		}
	}()

	if err := setupDB(db); err != nil {
		log.Fatalf(`can't setup DB: %v`, err)
	}

	var count int64
	if err := db.QueryRow("SELECT count(*) FROM envelopes").Scan(&count); err != nil {
		log.Fatal(err)
	}
	log.Printf(`DB contains %d envelopes`, count)

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
	http.HandleFunc("/history", func (w http.ResponseWriter, r *http.Request) {
		handleHistory(db, w, r)
	})
	err = http.ListenAndServe("127.0.0.1:8081", nil)
	if err != nil {
		log.Printf(`HTTP died: %s`, err)
	}
}
