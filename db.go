package main

import (
	"database/sql"
	"log"

	"github.com/google/uuid"
	_ "github.com/mattn/go-sqlite3"
)

type DB struct {
	db *sql.DB
}

func OpenDB() (*DB, error) {
	db, err := sql.Open("sqlite3", "envelopes.sqlite")
	if err != nil {
		return nil, err
	}

	var count int64
	if err := db.QueryRow("SELECT count(*) FROM envelopes WHERE deleted = 'false'").Scan(&count); err != nil {
		return nil, err
	}
	log.Printf(`DB contains %d envelopes`, count)

	return &DB{db}, nil
}

func (d *DB) Close() error {
	return d.db.Close()
}

func (d *DB) Setup() error {
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
		 deleted BOOLEAN,
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

func (d *DB) DeleteEnvelope(id string) {
	/* TODO: make sure that id is a well formed UUID */

	_, err := d.db.Exec("UPDATE envelopes SET deleted = 'true' WHERE id = $1", id)
	if err != nil {
		log.Printf(`error deleting envelope: %s`, err)
	}

	_, err = d.db.Exec(`
		INSERT INTO history
		VALUES ($1, $2, '', 0, 0, datetime('now'), 'true', 0)`, uuid.New(), id)
	if err != nil {
		log.Printf(`error deleting envelope history: %s`, err)
	}
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
		WHERE id = $1 AND deleted = 'false'`, id).Scan(&e.Id, &e.Name, &e.Balance, &e.Target, &e.MonthTarget)
	if err == nil {
		return &e, nil
	}

	e.Id = uuid.New()
	if _, err := tx.Exec(`INSERT INTO envelopes VALUES ($1, "", 0, 0, 'false', 0)`, e.Id); err != nil {
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
		SELECT id, date, name, balance, target, monthtarget, deleted
		FROM history
		WHERE envelope = $1`, id)
	if err != nil {
		return nil, events, err
	}
	defer rows.Close()

	for rows.Next() {
		var e Event
		var eventId uuid.UUID
		if err := rows.Scan(&eventId, &e.Date, &e.Name, &e.Balance, &e.Target, &e.MonthTarget, &e.Deleted); err != nil {
			log.Printf(`can't scan event %s: %s`, eventId, err)
		}
		if e.Deleted {
			e.Name = envelope.Name
		}
		events = append(events, e)
	}

	return envelope, events, nil
}

func (d *DB) UpdateEnvelope(id uuid.UUID, name string, dBalance, dTarget, dMonthTarget int, relative bool) error {
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}

	env, err := d.envelopeWithTx(tx, id)
	if err != nil {
		return err
	}

	log.Printf(`updating DB: name='%s', balance='%d', target='%d', monthtarget='%d'`,
		env.Name, env.Balance, env.Target, env.MonthTarget)

	/* Make parameters relative if they aren't already */
	if !relative {
		dBalance -= env.Balance
		dTarget -= env.Target
		dMonthTarget -= env.MonthTarget
	}

	log.Printf(`rel: %v, dB: %d dT: %d dMT: %d`, relative, dBalance, dTarget, dMonthTarget)

	_, err = tx.Exec(`
		INSERT INTO history
		VALUES ($1, $2, $3, $4, $5, datetime('now'), 'false', $6)`,
		uuid.New(), env.Id, name, dBalance, dTarget, dMonthTarget)
	if err != nil {
		tx.Rollback()
		return err
	}

	if name == "" {
		name = env.Name
	}
	res, err := tx.Exec(`
		UPDATE envelopes
		SET name = $1, balance = $2, target = $3, monthtarget = $4
		WHERE id = $5`, name, env.Balance+dBalance, env.Target+dTarget, env.MonthTarget+dMonthTarget, env.Id)
	rows, _ := res.RowsAffected()
	log.Printf(`%d affected rows`, rows)

	if err != nil {
		tx.Rollback()
		return err
	}

	return tx.Commit()
}
