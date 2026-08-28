# watchglass

**changedetection.io for video streams.** Point it at any screen — a 3D
printer's LCD, a lab instrument, a server console — draw a region, and get a
push notification when that region's text or pixels change. Self-hosted, one
binary, nothing leaves your network.

> Early development. Core engine and web UI both work; RTSP/ffmpeg sources,
> MQTT/Home Assistant discovery, and Docker packaging are next.

## Quick start

1. Install [tesseract](https://github.com/tesseract-ocr/tesseract) (only
   needed for OCR triggers): `apt install tesseract-ocr` or
   `choco install tesseract`.
2. Copy `examples/config.yaml` to `config.yaml` (the default path the binary
   looks for), point `source` at your camera's snapshot URL, and adjust the
   region and trigger.
3. Run:

    go run ./cmd/watchglass -config config.yaml

   `-config` defaults to `config.yaml` in the working directory, so if you
   used that name you can omit the flag. Readings are logged to a SQLite
   database at `-db` (default `watchglass.db`), which is created
   automatically on first run.

## Web UI

watchglass serves a local dashboard while it runs — open http://127.0.0.1:8080.
Add a watch, open it, drag a rectangle over the part of the screen you care
about, and hit **Test this region** to see exactly what the OCR engine reads —
tune the preprocessing sliders (grayscale, invert, binarize, upscale) until
the text comes back clean, then **Save**. The page shows a live strip of
recent readings so you can verify triggers before trusting them.

The UI binds to localhost only by default. `-listen 0.0.0.0:8080` exposes it
on your network — there is no authentication yet, so put it behind a reverse
proxy if you do that. Watch `source` URLs are also fetched by the server, so
exposing the UI beyond localhost hands whoever reaches it a server-side fetch
primitive too — another reason to keep it behind localhost or an
authenticated reverse proxy.

## Trigger types

| type | fires when |
|---|---|
| `ocr_match` | region text matches a regex (edge-triggered) |
| `ocr_changed` | region text changes to a new stable value |
| `numeric` | a number extracted from region text crosses a threshold |
| `pixel_change` | at least `threshold`% of region pixels change |

All OCR triggers require `confirm` consecutive identical readings before a
state is believed (filmed screens flicker), and every trigger respects
`cooldown`.

The `numeric` trigger requires `op: gt` or `op: lt` to say which direction
crosses `threshold`. By default it parses the first number found in the OCR
text; set `pattern` to a regex to extract a specific value instead — if the
regex has a capture group, that group's text is parsed rather than the whole
match.

## RTSP cameras

v1 only consumes HTTP snapshot URLs directly — it doesn't speak RTSP. For an
RTSP-only camera, run [go2rtc](https://github.com/AlexxIT/go2rtc) alongside
watchglass and point `source` at its snapshot endpoint instead, e.g.
`http://<go2rtc-host>:1984/api/frame.jpeg?src=<stream-name>`.

## License

MIT
