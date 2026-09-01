package source

import (
	"fmt"

	"watchglass/internal/config"
)

// For builds the frame source a watch's URL calls for. Unknown schemes are
// an error here rather than a per-tick failure, so a mistyped source shows
// up when the watch starts instead of silently never firing.
func For(w config.Watch) (Source, error) {
	kind, err := config.SourceKind(w.Source)
	if err != nil {
		return nil, err
	}
	switch kind {
	case "http":
		return NewHTTPSnapshot(w.Source), nil
	case "ffmpeg":
		return NewFFmpeg(w.Source)
	}
	return nil, fmt.Errorf("watch %q: unhandled source kind %q", w.Name, kind)
}
