// Package history persists per-watch readings to SQLite so users can see
// WHY a trigger fired (the last-N strip in the future web UI reads this).
package history

import (
	"database/sql"
	"errors"
	"strings"
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
	// stateErr is why trigger state can't be kept (the database can be read
	// but not written, say). The trigger state methods then do nothing, so
	// watches run as they did before there was any.
	stateErr error
}

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	// SQLite is single-writer, and the binary runs one goroutine per watch
	// against this shared store; the modernc driver applies no default busy
	// timeout, so a second concurrent writer fails with SQLITE_BUSY instead
	// of waiting. Capping the pool at one connection serializes all access.
	db.SetMaxOpenConns(1)
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
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_readings_watch_id ON readings(watch, id)`); err != nil {
		db.Close()
		return nil, err
	}
	// Prune filters on ts alone (no watch predicate), so it needs its own
	// index rather than riding the (watch, id) one above.
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_readings_ts ON readings(ts)`); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db, stateErr: openTriggerState(db)}, nil
}

// openTriggerState makes sure the trigger_state table exists: one row per
// watch with what its trigger has to remember across a restart (see
// TriggerState). Readings are a log that retention prunes; this is state,
// and Prune never touches it. A database that can't take the table (read
// only) still opens: watchglass then runs without keeping trigger state,
// as it did before there was any (see TriggerStateErr).
func openTriggerState(db *sql.DB) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS trigger_state (
		watch       TEXT    PRIMARY KEY,
		fingerprint TEXT    NOT NULL,
		last_fired  INTEGER NOT NULL,
		has_stable  INTEGER NOT NULL,
		stable      TEXT    NOT NULL,
		delivered   INTEGER NOT NULL DEFAULT 1,
		updated     INTEGER NOT NULL
	)`); err != nil {
		return err
	}
	// The table's first version had no delivered column.
	if _, err := db.Exec(`SELECT delivered FROM trigger_state LIMIT 0`); err != nil {
		if _, err := db.Exec(`ALTER TABLE trigger_state ADD COLUMN delivered INTEGER NOT NULL DEFAULT 1`); err != nil {
			return err
		}
	}
	return nil
}

// TriggerStateErr says why the database can't keep trigger state, or nil
// if it can. Without it the trigger state methods do nothing: nothing is
// loaded, and saving succeeds without writing.
func (s *Store) TriggerStateErr() error { return s.stateErr }

// TriggerState is what one watch's trigger remembers across a restart.
type TriggerState struct {
	// Fingerprint identifies the question the state answers (what the watch
	// looks at and what it looks for). The store only keeps it; the runner
	// decides what a different one means.
	Fingerprint string
	// LastFired is when the watch last fired, to the nanosecond, so a
	// cooldown ends when it was going to. Zero if it never has.
	LastFired time.Time
	// Stable is the state the trigger had settled on, if any (HasStable):
	// trigger.State's.
	Stable    string
	HasStable bool
	// Delivered says the alert of the fire at LastFired went out: every
	// notify URL took it, or the watch had none to send it to. A fire is
	// saved before its alert is sent, so it starts out false, and
	// MarkDelivered sets it once the send succeeds.
	Delivered bool
}

// SaveTriggerState stores watch's trigger state, replacing what was there.
// Delivered is taken as given for a new fire time; for the fire time the row
// already has, it can only go from false to true, so saving a new settled
// state never undoes a MarkDelivered that happened in between.
func (s *Store) SaveTriggerState(watch string, st TriggerState) error {
	if s.stateErr != nil {
		return nil
	}
	var fired int64
	if !st.LastFired.IsZero() {
		fired = st.LastFired.UnixNano()
	}
	_, err := s.db.Exec(`INSERT INTO trigger_state (watch, fingerprint, last_fired, has_stable, stable, delivered, updated)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(watch) DO UPDATE SET fingerprint = excluded.fingerprint, last_fired = excluded.last_fired,
			has_stable = excluded.has_stable, stable = excluded.stable,
			delivered = CASE WHEN trigger_state.last_fired = excluded.last_fired
				THEN max(trigger_state.delivered, excluded.delivered) ELSE excluded.delivered END,
			updated = excluded.updated`,
		watch, st.Fingerprint, fired, boolInt(st.HasStable), st.Stable, boolInt(st.Delivered), time.Now().Unix())
	return err
}

// MarkDelivered records that the alert of watch's fire at fired went out.
// It changes nothing if the saved state is about another fire by now.
func (s *Store) MarkDelivered(watch string, fired time.Time) error {
	if s.stateErr != nil || fired.IsZero() {
		return nil
	}
	_, err := s.db.Exec(`UPDATE trigger_state SET delivered = 1, updated = ? WHERE watch = ? AND last_fired = ?`,
		time.Now().Unix(), watch, fired.UnixNano())
	return err
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// LoadTriggerState returns watch's saved trigger state, or false if it has
// none.
func (s *Store) LoadTriggerState(watch string) (TriggerState, bool, error) {
	if s.stateErr != nil {
		return TriggerState{}, false, nil
	}
	var st TriggerState
	var fired int64
	var has, delivered int
	err := s.db.QueryRow(`SELECT fingerprint, last_fired, has_stable, stable, delivered FROM trigger_state WHERE watch = ?`, watch).
		Scan(&st.Fingerprint, &fired, &has, &st.Stable, &delivered)
	if errors.Is(err, sql.ErrNoRows) {
		return TriggerState{}, false, nil
	}
	if err != nil {
		return TriggerState{}, false, err
	}
	if fired != 0 {
		st.LastFired = time.Unix(0, fired)
	}
	st.HasStable = has == 1
	st.Delivered = delivered == 1
	return st, true, nil
}

// KeepTriggerState deletes the trigger state of every watch not named in
// watches (a deleted or renamed watch's), returning how many rows went.
func (s *Store) KeepTriggerState(watches []string) (int64, error) {
	if s.stateErr != nil {
		return 0, nil
	}
	q := `DELETE FROM trigger_state`
	args := make([]any, len(watches))
	if len(watches) > 0 {
		q += ` WHERE watch NOT IN (?` + strings.Repeat(`, ?`, len(watches)-1) + `)`
		for i, w := range watches {
			args[i] = w
		}
	}
	res, err := s.db.Exec(q, args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// LastPolled is when watch last recorded a reading: the last time it read
// its region, to the second. False if it has no readings (none yet, or all
// pruned).
func (s *Store) LastPolled(watch string) (time.Time, bool, error) {
	var ts int64
	err := s.db.QueryRow(`SELECT ts FROM readings WHERE watch = ? ORDER BY id DESC LIMIT 1`, watch).Scan(&ts)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, err
	}
	return time.Unix(ts, 0), true, nil
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

// Prune deletes readings recorded before the given time, returning how many
// rows were removed. Retention keeps the database bounded on long-running
// installs (one row per tick per watch adds up). It leaves trigger_state
// alone: that is one row per watch, and KeepTriggerState drops the ones
// whose watch is gone.
func (s *Store) Prune(before time.Time) (int64, error) {
	res, err := s.db.Exec(`DELETE FROM readings WHERE ts < ?`, before.Unix())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (s *Store) Close() error { return s.db.Close() }
