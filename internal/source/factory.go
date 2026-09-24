package source

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/darrenhuai/watchglass/internal/config"
	"github.com/darrenhuai/watchglass/internal/demo"
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
		opt := HTTPOptions{TLSInsecure: w.TLSInsecure}
		if len(w.Headers) > 0 {
			opt.Headers = http.Header{}
			for _, h := range w.Headers {
				name, value, err := config.ParseHeader(h)
				if err != nil {
					return nil, fmt.Errorf("watch %q: headers: %w", w.Name, err)
				}
				opt.Headers.Add(name, value)
			}
		}
		return NewHTTPSource(w.Source, opt), nil
	case "ffmpeg":
		return NewFFmpeg(w.Source)
	case "demo":
		return demo.NewSource(strings.TrimPrefix(w.Source, demo.Prefix))
	}
	return nil, fmt.Errorf("watch %q: unhandled source kind %q", w.Name, kind)
}
