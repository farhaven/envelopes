package main

import (
	"fmt"
	"html/template"
	"log"
	"net/http"
	"strconv"
	"sync"

	"database/sql"

	"github.com/google/uuid"
	_ "github.com/mattn/go-sqlite3"
)

var templFuncs = template.FuncMap{
	"prettyDisplay": prettyDisplay,
	"delta":         computeDelta,
}
var templ = template.Must(template.New("").Funcs(templFuncs).ParseGlob("templates/*.html"))

type Envelope struct {
	// Values in Euro-cents
	Id         string
	Balance    int
	Target     int
	Name       string
	MonthDelta int
	m          sync.Mutex
}

func (e *Envelope) String() string {
	return fmt.Sprintf("<Envelope '%s', Balance: %d, Target: %d>", e.Name, e.Balance, e.Target)
}

func EnvelopeFromDB(tx *sql.Tx, id string) *Envelope {
	e := Envelope{Id: id}

	if id != "" {
		err := tx.QueryRow("SELECT id, balance, target FROM envelopes WHERE id = $1", id).Scan(&e.Id, &e.Balance, &e.Target)
		if err == nil {
			return &e
		}
	}

	e.Id = uuid.New().String()
	if _, err := tx.Exec(`INSERT INTO envelopes VALUES ($1, "", 0, 0)`, e.Id); err != nil {
		log.Printf(`db insert failed: %s`, err)
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

	rows, err := db.Query(`
		SELECT e.id, e.name, e.balance, e.target, h.balance
		FROM envelopes AS e LEFT OUTER JOIN
			(SELECT envelope, sum(balance) AS balance, date
			 FROM history
			 WHERE date > DATE('now', 'start of month')
			 GROUP BY envelope) AS h
		ON e.id = h.envelope`)
	if err != nil {
		log.Printf(`error querying DB: %v`, err)
		return nil
	}
	defer rows.Close()

	for rows.Next() {
		var e Envelope
		var delta sql.NullInt64
		if err := rows.Scan(&e.Id, &e.Name, &e.Balance, &e.Target, &delta); err != nil {
			log.Printf(`error querying DB: %v`, err)
			return nil
		}
		if delta.Valid {
			e.MonthDelta = int(delta.Int64)
		}
		rv = append(rv, &e)
	}

	return rv
}

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

func handleDeleteRequest(db *sql.DB, w http.ResponseWriter, r *http.Request) {
	log.Printf(`delete: %v`, r.URL)
	log.Printf(`id: %s`, r.FormValue("id"))

	id := r.FormValue("id")

	_, err := db.Exec("DELETE FROM envelopes WHERE id = $1", id)
	if err != nil {
		log.Printf(`err: %s`, err)
	}

	_, err = db.Exec("DELETE FROM history WHERE envelope = $1", id)
	if err != nil {
		log.Printf(`err: %s`, err)
	}

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func handleUpdateRequest(db *sql.DB, w http.ResponseWriter, r *http.Request) {
	log.Printf(`update: %v`, r.URL)

	log.Printf(`name: %s`, r.FormValue("env-name"))
	log.Printf(`target: %s`, r.FormValue("env-target"))
	log.Printf(`balance: %s`, r.FormValue("env-balance"))
	log.Printf(`return: %s`, r.FormValue("env-return"))

	returnTo := "/"
	if r.FormValue("env-return") != "" {
		returnTo = "/details?id=" + r.FormValue("env-return")
	}

	id := r.FormValue("env-id")
	name := r.FormValue("env-name")

	tx, err := db.Begin()
	if err != nil {
		log.Printf(`can't start transaction: %s`, err)
		http.Redirect(w, r, returnTo, http.StatusSeeOther)
		return
	}
	env := EnvelopeFromDB(tx, id)

	if name != "" {
		env.Name = name
	}

	deltaBalance := 0
	bal, err := strconv.ParseFloat(r.FormValue("env-balance"), 64)
	if err != nil {
		log.Printf(`err: %s`, err)
	} else {
		deltaBalance = int(bal*100) - env.Balance
		env.Balance += deltaBalance
	}

	deltaTarget := 0
	tgt, err := strconv.ParseFloat(r.FormValue("env-target"), 64)
	if err != nil {
		log.Printf(`err: %s`, err)
	} else {
		deltaTarget = int(tgt*100) - env.Target
		env.Target += deltaTarget
	}

	log.Printf(`updating DB: name='%s', balance='%d', target='%d'`, env.Name, env.Balance, env.Target)

	history_id := uuid.New().String()

	_, err = tx.Exec("INSERT INTO history VALUES ($1, $2, $3, $4, $5, datetime('now'))", history_id, env.Id, env.Name, deltaBalance, deltaTarget)
	if err != nil {
		log.Printf(`can't create history entry for change to envelope %s`, env.Id)
		http.Redirect(w, r, returnTo, http.StatusSeeOther)
		return
	}
	res, err := tx.Exec("UPDATE envelopes SET name = $1, balance = $2, target = $3 WHERE id = $4", env.Name, env.Balance, env.Target, env.Id)
	rows, _ := res.RowsAffected()
	log.Printf(`%d affected rows`, rows)

	if err != nil {
		log.Printf(`can't update envelope: %v`, err)
		http.Redirect(w, r, returnTo, http.StatusSeeOther)
		return
	}

	if err = tx.Commit(); err != nil {
		log.Printf(`can't commit transaction: %v`, err)
	}

	http.Redirect(w, r, returnTo, http.StatusSeeOther)
}

func handleDetail(db *sql.DB, w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("id")

	type Event struct {
		Date    string
		Name    string
		Balance int
		Target  int
	}

	param := struct {
		Id     string
		Name   string
		Target int
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

	if err := tx.QueryRow("SELECT name, target FROM envelopes WHERE id = $1", id).Scan(&param.Name, &param.Target); err != nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	rows, err := tx.Query("SELECT id, date, name, balance, target FROM history WHERE envelope = $1", id)
	if err != nil {
		log.Printf(`can't query history for envelope %s: %s`, id, err)
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	defer rows.Close()

	for rows.Next() {
		var e Event
		var eventId string
		if err := rows.Scan(&eventId, &e.Date, &e.Name, &e.Balance, &e.Target); err != nil {
			log.Printf(`can't scan event %s: %s`, eventId, err)
		}
		param.Events = append(param.Events, e)
	}

	if err := templ.ExecuteTemplate(w, "details.html", param); err != nil {
		log.Printf(`error rendering details template: %s`, err)
	}
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
		Envelopes  []*Envelope
		TotalDelta struct {
			Cls string
			Val int
		}
		TotalBalance int
	}{
		Envelopes: es,
		TotalDelta: struct {
			Cls string
			Val int
		}{dcls, delta},
		TotalBalance: balance,
	}

	if err := templ.ExecuteTemplate(w, "index.html", param); err != nil {
		log.Printf(`error rendering overview template: %s`, err)
	}
}

func setupDB(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	if _, err := tx.Exec("CREATE TABLE IF NOT EXISTS envelopes (id UUID PRIMARY KEY, name STRING, balance INTEGER, target INTEGER)"); err != nil {
		return err
	}
	if _, err := tx.Exec("CREATE TABLE IF NOT EXISTS history (id UUID PRIMARY KEY AUTOINCREMENT, envelope UUID, date DATETIME, name STRING, balance INTEGER, target INTEGER, FOREIGN KEY(envelope) REFERENCES envelopes(id))"); err != nil {
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
	err = http.ListenAndServe("127.0.0.1:8081", nil)
	if err != nil {
		log.Printf(`HTTP died: %s`, err)
	}
}
