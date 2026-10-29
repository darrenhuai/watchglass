package history

import (
	"database/sql"
	"errors"
	"time"
)

// A value watch (and a numeric one) records the number it published with
// each reading, in the readings table's value column, so the readings can
// be taken out as numbers (ReadingsPage, the web UI's history.csv). A
// counter also keeps the last value it published in counter_state, one
// row per watch, so a restart still holds back a lower reading; and a
// lower value someone accepted on the watch page (a replaced meter) waits
// there until the runner takes it.

// openMeter adds the readings table's value column (a database from
// v0.1.12 or before has none) and makes the counter_state table. It runs
// with the other tables (openTables), so a lock at open is tried again
// the same way.
func openMeter(db *sql.DB) error {
	if !hasValueColumn(db) {
		if _, err := db.Exec(`ALTER TABLE readings ADD COLUMN value REAL`); err != nil {
			return err
		}
	}
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS counter_state (
		watch      TEXT    PRIMARY KEY,
		last       REAL    NOT NULL,
		updated    INTEGER NOT NULL,
		has_accept INTEGER NOT NULL DEFAULT 0,
		accept     REAL    NOT NULL DEFAULT 0
	)`)
	return err
}

func hasValueColumn(db *sql.DB) bool {
	_, err := db.Exec(`SELECT value FROM readings LIMIT 0`)
	return err == nil
}

// RecordValue is Record for a reading that published a number: value is
// kept with it. A database that can't take the column (read only, from
// before it) records the reading without it.
func (s *Store) RecordValue(watch string, ts time.Time, reading string, value float64, fired bool) error {
	if s.writeErr != nil {
		return nil
	}
	if !s.valueColumn() {
		return s.Record(watch, ts, reading, fired)
	}
	_, err := s.db.Exec(`INSERT INTO readings (watch, ts, reading, fired, value) VALUES (?, ?, ?, ?, ?)`,
		watch, ts.Unix(), reading, boolInt(fired), value)
	return err
}

// valueColumn reports whether the readings table has the value column,
// trying a step Open met a lock on again first.
func (s *Store) valueColumn() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lockErr != nil {
		s.openTables()
	}
	if !s.valueCol {
		s.valueCol = hasValueColumn(s.db)
	}
	return s.valueCol
}

// Row is one recorded reading as ReadingsPage returns it: the value it
// published, if any (HasValue), and its id, to ask for the next page.
type Row struct {
	ID       int64
	TS       time.Time
	Reading  string
	Fired    bool
	Value    float64
	HasValue bool
}

// ReadingsPage returns up to limit of watch's readings after the one with
// id after (0 for the first page), oldest first, recorded at from or
// later and before to (a zero time leaves that end open). A caller that
// streams a long history asks page by page, so neither the rows nor the
// database's one connection are held while it writes them out.
func (s *Store) ReadingsPage(watch string, after int64, from, to time.Time, limit int) ([]Row, error) {
	valueExpr := "NULL"
	if s.valueColumn() {
		valueExpr = "value"
	}
	q := `SELECT id, ts, reading, fired, ` + valueExpr + ` FROM readings WHERE watch = ? AND id > ?`
	args := []any{watch, after}
	if !from.IsZero() {
		q += ` AND ts >= ?`
		args = append(args, from.Unix())
	}
	if !to.IsZero() {
		q += ` AND ts < ?`
		args = append(args, to.Unix())
	}
	q += ` ORDER BY id LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Row, 0, limit)
	for rows.Next() {
		var r Row
		var ts int64
		var fired int
		var value sql.NullFloat64
		if err := rows.Scan(&r.ID, &ts, &r.Reading, &fired, &value); err != nil {
			return nil, err
		}
		r.TS = time.Unix(ts, 0)
		r.Fired = fired == 1
		r.Value, r.HasValue = value.Float64, value.Valid
		out = append(out, r)
	}
	return out, rows.Err()
}

// CounterState is what a counter keeps across a restart: the last value it
// published, and a lower value accepted on the watch page that it hasn't
// taken yet (HasAccept).
type CounterState struct {
	Last      float64
	HasLast   bool
	Accept    float64
	HasAccept bool
}

// SaveCounterLast stores the last value watch's counter published. It
// leaves an accept that is waiting alone.
func (s *Store) SaveCounterLast(watch string, last float64) error {
	if ok, err := s.tables(); !ok {
		return err
	}
	_, err := s.db.Exec(`INSERT INTO counter_state (watch, last, updated) VALUES (?, ?, ?)
		ON CONFLICT(watch) DO UPDATE SET last = excluded.last, updated = excluded.updated`,
		watch, last, time.Now().Unix())
	return err
}

// SaveCounterAccept stores a lower value accepted for watch's counter, for
// the runner to take (TakeCounterAccept). It replaces one still waiting.
func (s *Store) SaveCounterAccept(watch string, v float64) error {
	if ok, err := s.tables(); !ok {
		return err
	}
	_, err := s.db.Exec(`INSERT INTO counter_state (watch, last, updated, has_accept, accept) VALUES (?, ?, ?, 1, ?)
		ON CONFLICT(watch) DO UPDATE SET has_accept = 1, accept = excluded.accept, updated = excluded.updated`,
		watch, v, time.Now().Unix(), v)
	return err
}

// TakeCounterAccept clears the accepted value accept, if it is still the
// one waiting, and stores last as watch's counter's last value: accept
// itself when the runner took it, or the value it kept when accept was no
// longer lower (the display had come back up).
func (s *Store) TakeCounterAccept(watch string, accept, last float64) error {
	if ok, err := s.tables(); !ok {
		return err
	}
	_, err := s.db.Exec(`UPDATE counter_state SET last = ?, has_accept = 0, updated = ? WHERE watch = ? AND has_accept = 1 AND accept = ?`,
		last, time.Now().Unix(), watch, accept)
	return err
}

// LoadCounter returns watch's saved counter state, or false if it has
// none.
func (s *Store) LoadCounter(watch string) (CounterState, bool, error) {
	if !s.meterReadable() {
		return CounterState{}, false, nil
	}
	var c CounterState
	var hasAccept int
	err := s.db.QueryRow(`SELECT last, has_accept, accept FROM counter_state WHERE watch = ?`, watch).
		Scan(&c.Last, &hasAccept, &c.Accept)
	if errors.Is(err, sql.ErrNoRows) {
		return CounterState{}, false, nil
	}
	if err != nil {
		return CounterState{}, false, err
	}
	c.HasLast = true
	c.HasAccept = hasAccept == 1
	return c, true, nil
}

// DropCounter forgets watch's counter state: the watch is no longer a
// counter.
func (s *Store) DropCounter(watch string) error {
	if ok, err := s.tables(); !ok {
		return err
	}
	_, err := s.db.Exec(`DELETE FROM counter_state WHERE watch = ?`, watch)
	return err
}

// meterReadable reports whether counter_state is there to read.
func (s *Store) meterReadable() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lockErr != nil {
		s.openTables()
	}
	return s.meterRead
}
