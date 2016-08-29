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
	Id         uuid.UUID
	Balance    int
	Target     int
	Name       string
	MonthDelta int
	MonthTarget int
	m          sync.Mutex
}

func (e *Envelope) String() string {
	return fmt.Sprintf("<Envelope '%s', Balance: %d, Target: %d>", e.Name, e.Balance, e.Target)
}

func EnvelopeFromDB(tx *sql.Tx, id uuid.UUID) *Envelope {
	e := Envelope{Id: id}

	err := tx.QueryRow(`
		SELECT id, name, balance, target, monthtarget
		FROM envelopes
		WHERE id = $1 AND deleted = 'false'`, id).Scan(&e.Id, &e.Name, &e.Balance, &e.Target, &e.MonthTarget)
	if err == nil {
		return &e
	}

	e.Id = uuid.New()
	if _, err := tx.Exec(`INSERT INTO envelopes VALUES ($1, "", 0, 0, 'false', 0)`, e.Id); err != nil {
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
		SELECT e.id, e.name, e.balance, e.target, e.monthtarget, h.balance
		FROM envelopes AS e LEFT OUTER JOIN
			(SELECT envelope, sum(balance) AS balance, date
			 FROM history
			 WHERE date > DATE('now', 'start of month')
			 GROUP BY envelope) AS h
		ON e.id = h.envelope
		WHERE e.deleted = 'false'`)
	if err != nil {
		log.Printf(`error querying DB: %v`, err)
		return nil
	}
	defer rows.Close()

	for rows.Next() {
		var e Envelope
		var delta sql.NullInt64
		if err := rows.Scan(&e.Id, &e.Name, &e.Balance, &e.Target, &e.MonthTarget, &delta); err != nil {
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

	_, err := db.Exec("UPDATE envelopes SET deleted = 'true' WHERE id = $1", id)
	if err != nil {
		log.Printf(`error deleting envelope: %s`, err)
	}

	_, err = db.Exec(`
		INSERT INTO history
		VALUES ($1, $2, '', 0, 0, datetime('now'), 'true', 0)`, uuid.New(), id)
	if err != nil {
		log.Printf(`error deleting envelope history: %s`, err)
	}

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func handleUpdateRequest(db *sql.DB, w http.ResponseWriter, r *http.Request) {
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

	tx, err := db.Begin()
	if err != nil {
		log.Printf(`can't start transaction: %s`, err)
		http.Redirect(w, r, returnTo, http.StatusSeeOther)
		return
	}
	env := EnvelopeFromDB(tx, id)

	name := r.FormValue("env-name")
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

	deltaMonthTarget := 0
	monthtgt, err := strconv.ParseFloat(r.FormValue("env-monthtarget"), 64)
	if err != nil {
		log.Printf(`err: %s`, err)
	} else {
		deltaMonthTarget = int(monthtgt*100) - env.MonthTarget
		env.MonthTarget += deltaMonthTarget
	}

	log.Printf(`updating DB: name='%s', balance='%d', target='%d', monthtarget='%d', dt='%d'`,
	           env.Name, env.Balance, env.Target, env.MonthTarget, deltaMonthTarget)

	_, err = tx.Exec(`
		INSERT INTO history
		VALUES ($1, $2, $3, $4, $5, datetime('now'), 'false', $6)`,
		uuid.New(), env.Id, env.Name, deltaBalance, deltaTarget, deltaMonthTarget)
	if err != nil {
		log.Printf(`can't create history entry for change to envelope %s: %s`, env.Id, err)
		http.Redirect(w, r, returnTo, http.StatusSeeOther)
		return
	}
	res, err := tx.Exec(`
		UPDATE envelopes
		SET name = $1, balance = $2, target = $3, monthtarget = $4
		WHERE id = $5`, env.Name, env.Balance, env.Target, env.MonthTarget, env.Id)
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
	log.Printf(`handling detail for id %s`, r.FormValue("id"))
	id, err := uuid.Parse(r.FormValue("id"))
	if err != nil {
		log.Printf(`detail: can't parse ID: %s`, err)
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	type Event struct {
		Date    string
		Name    string
		Balance int
		Target  int
		MonthTarget int
		Deleted bool
	}

	param := struct {
		Envelope *Envelope
		Events []Event
	}{}

	tx, err := db.Begin()
	if err != nil {
		log.Printf(`tx: %s`, err)
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	defer tx.Rollback()

	param.Envelope = EnvelopeFromDB(tx, id)

	rows, err := tx.Query(`
		SELECT id, date, name, balance, target, monthtarget, deleted
		FROM history
		WHERE envelope = $1`, id)
	if err != nil {
		log.Printf(`can't query history for envelope %s: %s`, id, err)
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	defer rows.Close()

	for rows.Next() {
		var e Event
		var eventId uuid.UUID
		if err := rows.Scan(&eventId, &e.Date, &e.Name, &e.Balance, &e.Target, &e.MonthTarget, &e.Deleted); err != nil {
			log.Printf(`can't scan event %s: %s`, eventId, err)
		}
		if e.Deleted {
			e.Name = param.Envelope.Name
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

	if _, err := tx.Exec(`
		CREATE TABLE IF NOT EXISTS envelopes
		(id UUID PRIMARY KEY, name STRING,
		 balance INTEGER,
		 target INTEGER, monthtarget INTEGER,
		 deleted BOOLEAN)`); err != nil {
		return err
	}

	if _, err := tx.Exec(`
		CREATE TABLE IF NOT EXISTS history
		(id UUID PRIMARY KEY AUTOINCREMENT,
		 envelope UUID, date DATETIME, name STRING,
		 balance INTEGER, target INTEGER, monthtarget INTEGER,
		 deleted BOOLEAN,
		 FOREIGN KEY(envelope) REFERENCES envelopes(id))`); err != nil {
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
	if err := db.QueryRow("SELECT count(*) FROM envelopes WHERE deleted = 'false'").Scan(&count); err != nil {
		log.Fatal(err)
	}
	log.Printf(`DB contains %d envelopes`, count)

	pm := PeerManager{}
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
	err = http.ListenAndServe("127.0.0.1:8081", nil)
	if err != nil {
		log.Printf(`HTTP died: %s`, err)
	}
}
