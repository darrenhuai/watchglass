// Package history persists per-watch readings to SQLite so users can see
// WHY a trigger fired (the last-N strip in the future web UI reads this).
package history

import (
	"database/sql"
	"time"

	_ "modernc.org/sqlite" // pure-Go driver, keeps CGO_ENABLED=0
)

type Reading struct {
	Watch   string
	TS      time.Time
	Reading string
	Fired   bool
}

type Store struct {
	db *sql.DB
}

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS readings (
		id      INTEGER PRIMARY KEY AUTOINCREMENT,
		watch   TEXT    NOT NULL,
		ts      INTEGER NOT NULL,
		reading TEXT    NOT NULL,
		fired   INTEGER NOT NULL
	)`)
	if err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Record(watch string, ts time.Time, reading string, fired bool) error {
	f := 0
	if fired {
		f = 1
	}
	_, err := s.db.Exec(`INSERT INTO readings (watch, ts, reading, fired) VALUES (?, ?, ?, ?)`,
		watch, ts.Unix(), reading, f)
	return err
}

func (s *Store) LastN(watch string, n int) ([]Reading, error) {
	rows, err := s.db.Query(
		`SELECT watch, ts, reading, fired FROM readings WHERE watch = ? ORDER BY id DESC LIMIT ?`,
		watch, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Reading
	for rows.Next() {
		var r Reading
		var ts int64
		var fired int
		if err := rows.Scan(&r.Watch, &ts, &r.Reading, &fired); err != nil {
			return nil, err
		}
		r.TS = time.Unix(ts, 0)
		r.Fired = fired == 1
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) Close() error { return s.db.Close() }
