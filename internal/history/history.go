// Package history persists per-watch readings to SQLite so users can see
// WHY a trigger fired (the last-N strip in the future web UI reads this).
package history

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"time"

	sqlite "modernc.org/sqlite" // pure-Go driver, keeps CGO_ENABLED=0
)

type Reading struct {
	Watch   string
	TS      time.Time
	Reading string
	Fired   bool
}

type Store struct {
	db *sql.DB
	// writeErr is why the database can't be written (opened read only, a
	// read-only file or folder), found once by Open. Nothing is written
	// then: Record, Prune and the trigger state methods do nothing, so a
	// read-only database costs one line at start (WriteErr) rather than an
	// error on every poll.
	writeErr error

	// mu guards the fields below: a schema step Open couldn't finish is
	// tried again by whichever watch's goroutine next uses them (tables).
	mu sync.Mutex
	// stateErr is why trigger state can't be kept: the table couldn't be
	// made, or the database can't be written. The methods that write it
	// then do nothing, so watches run as they did before there was any.
	// stateRead says the table is there to read (a read-only database
	// that has it still restores what it holds).
	stateErr  error
	stateRead bool
	// healthRead says the watch_health table is there (see HealthState).
	healthRead bool
	// lockErr is set while a table Open had to make met another
	// connection's write lock (a v0.1.9 database has no watch_health yet,
	// and a DB browser or backup may be writing it at that first start).
	// That isn't a reason to keep nothing for the whole run: each use tries
	// the step again, and a write fails with the lock until it succeeds,
	// as any write meeting the lock does.
	lockErr error
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
	st := &Store{db: db, writeErr: probeWrite(db)}
	st.openTables()
	return st, nil
}

// openTables makes the trigger_state and watch_health tables and sets
// what can be kept from how that went (stateErr, stateRead, healthRead,
// lockErr). It is run by Open, and again by tables while lockErr is set.
// s.mu must be held once s is shared.
func (s *Store) openTables() {
	tableErr := openTriggerState(s.db)
	healthErr := openHealth(s.db)
	s.stateRead = s.stateRead || tableErr == nil
	s.healthRead = s.healthRead || healthErr == nil
	s.lockErr = nil
	switch {
	case s.writeErr != nil:
		s.stateErr = s.writeErr
	case tableErr != nil && !lockedByAnother(tableErr):
		s.stateErr = tableErr
	case healthErr != nil && !lockedByAnother(healthErr):
		s.stateErr = healthErr
	case tableErr != nil:
		s.lockErr = tableErr
	case healthErr != nil:
		s.lockErr = healthErr
	}
}

// tables reports whether trigger state and stream health can be written
// now. It is nil, true when they can; nil, false where they can't be kept
// at all (TriggerStateErr), so the caller writes nothing and says nothing;
// and the lock's error while a table Open had to make still waits for
// another connection to finish writing (lockErr), which it tries again
// first.
func (s *Store) tables() (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lockErr != nil {
		s.openTables()
	}
	switch {
	case s.stateErr != nil:
		return false, nil
	case s.lockErr != nil:
		return false, s.lockErr
	}
	return true, nil
}

// readable reports whether the trigger_state and watch_health tables are
// there to read, trying a table Open met a lock on again first.
func (s *Store) readable() (state, health bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lockErr != nil {
		s.openTables()
	}
	return s.stateRead, s.healthRead
}

// openHealth makes sure the watch_health table exists: one row per watch
// with its last stream health verdict (HealthState), kept beside the
// trigger state for the same reason: so a restart doesn't send a camera's
// "down" alert again while the camera is still down.
func openHealth(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS watch_health (
		watch   TEXT    PRIMARY KEY,
		down    INTEGER NOT NULL,
		since   INTEGER NOT NULL,
		message TEXT    NOT NULL
	)`)
	return err
}

// HealthState is a watch's stream health verdict as the last transition
// left it: down (and since when, and why) or up again.
type HealthState struct {
	Down    bool
	Since   time.Time
	Message string
}

// SaveHealth stores watch's health verdict, replacing what was there. It
// writes nothing where trigger state can't be kept (TriggerStateErr).
func (s *Store) SaveHealth(watch string, h HealthState) error {
	if ok, err := s.tables(); !ok {
		return err
	}
	_, err := s.db.Exec(`INSERT INTO watch_health (watch, down, since, message) VALUES (?, ?, ?, ?)
		ON CONFLICT(watch) DO UPDATE SET down = excluded.down, since = excluded.since, message = excluded.message`,
		watch, boolInt(h.Down), h.Since.UnixNano(), h.Message)
	return err
}

// LoadHealth returns watch's saved health verdict, or false if it has none.
func (s *Store) LoadHealth(watch string) (HealthState, bool, error) {
	if _, health := s.readable(); !health {
		return HealthState{}, false, nil
	}
	var h HealthState
	var down int
	var since int64
	err := s.db.QueryRow(`SELECT down, since, message FROM watch_health WHERE watch = ?`, watch).Scan(&down, &since, &h.Message)
	if errors.Is(err, sql.ErrNoRows) {
		return HealthState{}, false, nil
	}
	if err != nil {
		return HealthState{}, false, err
	}
	h.Down = down == 1
	if since != 0 {
		h.Since = time.Unix(0, since)
	}
	return h, true, nil
}

// probeWrite reports whether the database takes a write, by making a table
// inside a transaction that is rolled back: a read-only connection refuses
// the BEGIN IMMEDIATE, and a read-only folder (no room for the journal) the
// CREATE. CREATE TABLE IF NOT EXISTS on tables that are already there
// writes nothing, so Open alone can't tell.
//
// A database another connection is writing at that moment (a DB browser,
// a backup, the previous watchglass still exiting) answers the BEGIN with
// SQLITE_BUSY: that says nothing about whether it can be written, so the
// probe counts it as writable. A write that then meets the lock fails on
// its own, as every write did before there was a probe, and the next one
// after the lock is gone succeeds.
func probeWrite(db *sql.DB) error {
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		if lockedByAnother(err) {
			return nil
		}
		return err
	}
	_, err = conn.ExecContext(ctx, `CREATE TABLE watchglass_write_probe (x INTEGER)`)
	if _, rerr := conn.ExecContext(ctx, `ROLLBACK`); err == nil {
		err = rerr
	}
	return err
}

// lockedByAnother reports SQLITE_BUSY or SQLITE_LOCKED (primary codes 5
// and 6; the extended codes keep them in the low byte): another connection
// holds the lock for now.
func lockedByAnother(err error) bool {
	var se *sqlite.Error
	if !errors.As(err, &se) {
		return false
	}
	switch se.Code() & 0xff {
	case 5, 6:
		return true
	}
	return false
}

// WriteErr says why the database can't be written, or nil if it can. When
// it can't, readings aren't recorded and trigger state isn't kept; what is
// already there can still be read.
func (s *Store) WriteErr() error { return s.writeErr }

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
// if it can. Without it saving succeeds without writing, and nothing is
// loaded unless the table is there to read (a read-only database).
func (s *Store) TriggerStateErr() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stateErr
}

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
	if ok, err := s.tables(); !ok {
		return err
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
	if fired.IsZero() {
		return nil
	}
	if ok, err := s.tables(); !ok {
		return err
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
	if state, _ := s.readable(); !state {
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

// KeepTriggerState deletes the trigger state and the health verdict of
// every watch not named in watches (a deleted or renamed watch's),
// returning how many rows went.
func (s *Store) KeepTriggerState(watches []string) (int64, error) {
	if ok, err := s.tables(); !ok {
		return 0, err
	}
	where := ""
	args := make([]any, len(watches))
	if len(watches) > 0 {
		where = ` WHERE watch NOT IN (?` + strings.Repeat(`, ?`, len(watches)-1) + `)`
		for i, w := range watches {
			args[i] = w
		}
	}
	var gone int64
	for _, table := range []string{"trigger_state", "watch_health"} {
		res, err := s.db.Exec(`DELETE FROM `+table+where, args...)
		if err != nil {
			return gone, err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return gone, err
		}
		gone += n
	}
	return gone, nil
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
	if s.writeErr != nil {
		return nil
	}
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
	if s.writeErr != nil {
		return 0, nil
	}
	res, err := s.db.Exec(`DELETE FROM readings WHERE ts < ?`, before.Unix())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (s *Store) Close() error { return s.db.Close() }
