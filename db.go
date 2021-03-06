package main

import (
	"database/sql"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"
	_ "github.com/mattn/go-sqlite3"
)

type Event struct {
	EnvelopeId  uuid.UUID
	Id          uuid.UUID
	Date        string
	Name        string
	Balance     int
	Target      int
	MonthTarget int
	Deleted     bool
	Comment     string
}

type Envelope struct {
	// Values in Euro-cents
	Id          uuid.UUID
	Balance     int
	Target      int
	Name        string
	MonthDelta  int
	MonthTarget int
}

type DB struct {
	db     *sql.DB
	Events chan Event
}

func OpenDB() (*DB, error) {
	db, err := sql.Open("sqlite3", "envelopes.sqlite")
	if err != nil {
		return nil, err
	}

	rv := &DB{db, make(chan Event)}

	if err := rv.setup(); err != nil {
		return nil, err
	}

	var count int64
	if err := db.QueryRow("SELECT count(*) FROM envelopes WHERE not deleted").Scan(&count); err != nil {
		return nil, err
	}
	log.Printf(`DB contains %d envelopes`, count)

	return rv, nil
}

func (d *DB) Close() error {
	return d.db.Close()
}

func (d *DB) setup() error {
	tx, err := d.db.Begin()
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
		(id UUID PRIMARY KEY,
		 envelope UUID, date DATETIME, name STRING,
		 balance INTEGER, target INTEGER, monthtarget INTEGER,
		 deleted BOOLEAN, comment STRING,
		 FOREIGN KEY(envelope) REFERENCES envelopes(id))`); err != nil {
		return err
	}

	return tx.Commit()
}

func (d *DB) AllEnvelopes() []*Envelope {
	rv := []*Envelope{}

	rows, err := d.db.Query(`
		SELECT e.id, e.name, e.balance, e.target, e.monthtarget, h.balance
		FROM envelopes AS e LEFT OUTER JOIN
			(SELECT envelope, sum(balance) AS balance, date
			 FROM history
			 WHERE date > DATE('now', 'start of month')
			 GROUP BY envelope) AS h
		ON e.id = h.envelope
		WHERE not e.deleted`)
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

func (d *DB) DeleteEnvelope(id uuid.UUID) error {
	evt := Event{
		EnvelopeId: id,
		Id:         uuid.New(),
		Date:       time.Now().String(),
		Deleted:    true,
	}

	select {
	case d.Events <- evt:
		/* Nothing */
	default:
		/* Nothing */
	}

	return d.MergeEvent(evt)
}

func (d *DB) Envelope(id uuid.UUID) (*Envelope, error) {
	tx, err := d.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	return d.envelopeWithTx(tx, id)
}

func (d *DB) envelopeWithTx(tx *sql.Tx, id uuid.UUID) (*Envelope, error) {
	e := Envelope{Id: id}

	err := tx.QueryRow(`
		SELECT id, name, balance, target, monthtarget
		FROM envelopes
		WHERE id = $1 AND not deleted`, id).Scan(&e.Id, &e.Name, &e.Balance, &e.Target, &e.MonthTarget)
	if err == nil {
		return &e, nil
	}

	if _, err := tx.Exec(`
		INSERT INTO envelopes(id, name, balance, target, monthtarget, deleted)
		VALUES ($1, "", 0, 0, 0, 'false')`, e.Id); err != nil {
		return nil, err
	}
	return &e, nil
}

func (d *DB) EnvelopeWithHistory(id uuid.UUID) (*Envelope, []Event, error) {
	events := []Event{}

	tx, err := d.db.Begin()
	if err != nil {
		return nil, events, err
	}
	defer tx.Rollback()

	envelope, err := d.envelopeWithTx(tx, id)
	if err != nil {
		return nil, events, err
	}

	rows, err := tx.Query(`
		SELECT id, envelope, date, name, balance, target, monthtarget, comment, deleted
		FROM history
		WHERE envelope = $1`, id)
	if err != nil {
		return nil, events, err
	}
	defer rows.Close()

	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.Id, &e.EnvelopeId, &e.Date, &e.Name, &e.Balance, &e.Target, &e.MonthTarget, &e.Comment, &e.Deleted); err != nil {
			log.Printf(`can't scan event %s: %s`, e.Id, err)
		}
		if e.Deleted {
			e.Name = envelope.Name
		}
		events = append(events, e)
	}

	return envelope, events, nil
}

func (d *DB) MergeEvent(e Event) error {
	log.Printf(`merging event %v`, e.Id)

	tx, err := d.db.Begin()
	if err != nil {
		return err
	}

	env, err := d.envelopeWithTx(tx, e.EnvelopeId)
	if err != nil {
		tx.Rollback()
		return err
	}

	_, err = tx.Exec(`
		INSERT INTO history (id, envelope, name, balance, target, monthtarget, comment, deleted, date)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, datetime('now'))`,
		e.Id, e.EnvelopeId, e.Name, e.Balance, e.Target, e.MonthTarget, e.Comment, e.Deleted)
	if err != nil {
		tx.Rollback()
		return err
	}

	if e.Name == "" {
		e.Name = env.Name
	}
	_, err = tx.Exec(`
		UPDATE envelopes
		SET name = $1, balance = $2, target = $3, monthtarget = $4, deleted = $5
		WHERE id = $6`, e.Name, env.Balance+e.Balance, env.Target+e.Target, env.MonthTarget+e.MonthTarget, e.Deleted, env.Id)
	if err != nil {
		tx.Rollback()
		return err
	}

	return tx.Commit()
}

func (d *DB) UpdateEnvelopeMeta(id uuid.UUID, name string, newTarget, newMonthTarget int) error {
	env, err := d.Envelope(id)
	if err != nil {
		return err
	}

	if name == env.Name && newTarget == 0 && newMonthTarget == 0 {
		return nil
	}

	log.Printf(`dB update meta: dT: %d dMT: %d`, newTarget, newMonthTarget)

	evt := Event{
		EnvelopeId:  env.Id,
		Id:          uuid.New(),
		Date:        time.Now().String(),
		Name:        name,
		Balance:     0,
		Target:      newTarget - env.Target,
		MonthTarget: newMonthTarget - env.MonthTarget,
		Deleted:     false,
		Comment:     "",
	}

	select {
	case d.Events <- evt:
		/* nothing */
	default:
		/* nothing */
	}

	return d.MergeEvent(evt)
}

func (d *DB) UpdateEnvelopeBalance(id uuid.UUID, dBalance int, comment string) error {
	env, err := d.Envelope(id)
	if err != nil {
		return err
	}

	log.Printf(`dB update balance: %d`, dBalance)

	evt := Event{
		EnvelopeId:  env.Id,
		Id:          uuid.New(),
		Date:        time.Now().String(),
		Name:        env.Name,
		Balance:     dBalance,
		Target:      0,
		MonthTarget: 0,
		Deleted:     false,
		Comment:     comment,
	}

	select {
	case d.Events <- evt:
		/* nothing */
	default:
		/* nothing */
	}

	return d.MergeEvent(evt)
}

func (d *DB) Spread(id uuid.UUID) error {
	es := d.AllEnvelopes()
	toSpread, err := d.Envelope(id)

	if err != nil {
		return err
	}

	totalMonthTarget := int(0)
	for _, e := range es {
		if e.Id == id {
			continue
		}
		totalMonthTarget += e.MonthTarget
	}

	for _, e := range es {
		if e.Id == id || e.MonthTarget == 0 {
			continue
		}

		pct := float64(e.MonthTarget) / float64(totalMonthTarget)
		amount := float64(toSpread.Balance) * pct

		if err := d.UpdateEnvelopeBalance(e.Id, int(amount), fmt.Sprintf(`Spread from %s`, toSpread.Name)); err != nil {
			return err
		}
		if err := d.UpdateEnvelopeBalance(id, int(-amount), fmt.Sprintf(`Spread to %s`, e.Name)); err != nil {
			return err
		}
	}

	return nil
}
