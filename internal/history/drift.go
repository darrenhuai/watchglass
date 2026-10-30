package history

import (
	"database/sql"
	"encoding/json"
	"errors"
	"image"
	"strings"
	"time"

	"github.com/darrenhuai/watchglass/internal/drift"
)

// A watch's position check (internal/drift) keeps its reference picture
// and what it last said here, one row per watch, beside the trigger
// state: so a restart neither forgets where the picture was (a camera
// bumped while watchglass was off still shows as moved) nor says a move
// it already said. The table is made on the first save; a database
// without it has nothing saved.

func openDrift(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS drift_ref (
		watch   TEXT    PRIMARY KEY,
		meta    TEXT    NOT NULL,
		gray    BLOB    NOT NULL,
		updated INTEGER NOT NULL
	)`)
	return err
}

// driftMeta is a drift.Saved without its pixels, as the meta column
// holds it.
type driftMeta struct {
	Key    string    `json:"key"`
	FrameW int       `json:"frame_w"`
	FrameH int       `json:"frame_h"`
	Box    [4]int    `json:"box"`
	Area   [4]int    `json:"area"`
	Scale  int       `json:"scale"`
	W      int       `json:"w"`
	H      int       `json:"h"`
	BaseX  float64   `json:"base_x"`
	BaseY  float64   `json:"base_y"`
	State  string    `json:"state"`
	DX     float64   `json:"dx"`
	DY     float64   `json:"dy"`
	Since  time.Time `json:"since"`
	Shake  float64   `json:"shake"`
}

func rect4(r image.Rectangle) [4]int  { return [4]int{r.Min.X, r.Min.Y, r.Max.X, r.Max.Y} }
func unrect(a [4]int) image.Rectangle { return image.Rect(a[0], a[1], a[2], a[3]) }

// SaveDrift stores watch's position check, replacing what was there. It
// writes nothing where trigger state can't be kept (TriggerStateErr).
func (s *Store) SaveDrift(watch string, sv drift.Saved) error {
	if ok, err := s.tables(); !ok {
		return err
	}
	if err := openDrift(s.db); err != nil {
		return err
	}
	m := driftMeta{Key: sv.Key, FrameW: sv.Ref.FrameW, FrameH: sv.Ref.FrameH, Box: rect4(sv.Ref.Box), Area: rect4(sv.Ref.Area),
		Scale: sv.Ref.Scale, W: sv.Ref.W, H: sv.Ref.H, BaseX: sv.BaseX, BaseY: sv.BaseY,
		State: string(sv.State), DX: sv.DX, DY: sv.DY, Since: sv.Since, Shake: sv.Shake}
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO drift_ref (watch, meta, gray, updated) VALUES (?, ?, ?, ?)
		ON CONFLICT(watch) DO UPDATE SET meta = excluded.meta, gray = excluded.gray, updated = excluded.updated`,
		watch, string(b), sv.Ref.Gray, time.Now().Unix())
	return err
}

// LoadDrift returns watch's saved position check, or false if it has
// none.
func (s *Store) LoadDrift(watch string) (drift.Saved, bool, error) {
	var meta string
	var gray []byte
	err := s.db.QueryRow(`SELECT meta, gray FROM drift_ref WHERE watch = ?`, watch).Scan(&meta, &gray)
	if errors.Is(err, sql.ErrNoRows) || (err != nil && strings.Contains(err.Error(), "no such table")) {
		return drift.Saved{}, false, nil
	}
	if err != nil {
		return drift.Saved{}, false, err
	}
	var m driftMeta
	if err := json.Unmarshal([]byte(meta), &m); err != nil {
		return drift.Saved{}, false, err
	}
	return drift.Saved{Key: m.Key, BaseX: m.BaseX, BaseY: m.BaseY, State: drift.State(m.State), DX: m.DX, DY: m.DY, Since: m.Since, Shake: m.Shake,
		Ref: drift.Reference{FrameW: m.FrameW, FrameH: m.FrameH, Box: unrect(m.Box), Area: unrect(m.Area),
			Scale: m.Scale, W: m.W, H: m.H, Gray: gray}}, true, nil
}

// KeepDrift deletes the saved position check of every watch not named
// in watches (a deleted or renamed watch's).
func (s *Store) KeepDrift(watches []string) error {
	if ok, err := s.tables(); !ok {
		return err
	}
	if err := openDrift(s.db); err != nil {
		return err
	}
	where := ""
	args := make([]any, len(watches))
	if len(watches) > 0 {
		where = ` WHERE watch NOT IN (?` + strings.Repeat(`, ?`, len(watches)-1) + `)`
		for i, w := range watches {
			args[i] = w
		}
	}
	_, err := s.db.Exec(`DELETE FROM drift_ref`+where, args...)
	return err
}
