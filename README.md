# watchglass

**changedetection.io for video streams.** Point it at any screen — a 3D
printer's LCD, a lab instrument, a server console — draw a region, and get a
push notification when that region's text or pixels change. Self-hosted, one
binary, nothing leaves your network.

> Early development. Core engine, web UI, RTSP/ffmpeg sources, and
> MQTT/Home Assistant discovery all work; Docker packaging is next.

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

## Sources

| Source string | What it reads | Needs |
|---|---|---|
| `http://…` / `https://…` | a snapshot/MJPEG URL (most IP cameras expose one) | nothing |
| `rtsp://…` / `rtsps://…` | an RTSP stream, over TCP | ffmpeg on PATH |
| `v4l2:/dev/video0` | a Linux webcam or capture card | ffmpeg on PATH |
| `dshow:video=Camera Name` | a Windows webcam or capture card | ffmpeg on PATH |
| `ffmpeg:<args>` | anything else — the args go to ffmpeg verbatim | ffmpeg on PATH |

For RTSP and device sources watchglass spawns ffmpeg once per poll, grabs a
single frame, and lets it exit. There is no persistent decoder, so a watch
costs nothing between checks. ffmpeg is never bundled — install your
distribution's package.

If a camera speaks something exotic (HomeKit, Nest, WebRTC-only), run
[go2rtc](https://github.com/AlexxIT/go2rtc) alongside and point watchglass at
its snapshot endpoint: `http://go2rtc-host:1984/api/frame.jpeg?src=cam1`.

### When a stream dies

After `health_after` consecutive failed grabs (default 3) a watch sends one
"stream unreachable" notification, and one more when it recovers. It never
repeats while a camera stays down, and it keeps polling throughout — a
watcher that silently stopped watching is worse than no watcher.

### Adaptive polling

Set `max_interval` above `interval` and a watch doubles its poll gap while
nothing changes, snapping back to `interval` the moment something does. It is
off unless you set it. Note that backing off also stretches how long
`confirm` takes in wall-clock time. A failed poll immediately returns the
watch to its base interval, so stream-death detection (`health_after`) is
never delayed by backoff.

## Home Assistant

Add an `mqtt:` block and every watch shows up in Home Assistant
automatically — no YAML on the HA side:

    mqtt:
      broker: tcp://homeassistant.local:1883
      username: watchglass
      password: secret

Each watch becomes a device with four entities via MQTT discovery: a
**reading** sensor (the latest OCR text or pixel-change percentage), a
**health** sensor (stream up/down), a **motion** sensor that pulses when the
trigger fires, and a **camera** showing the crop from the last fire. State
survives HA restarts (retained topics), and watchglass announces its own
availability with a last-will message, so entities go unavailable if it
stops.

Broker down? watchglass keeps watching and reconnects in the background —
MQTT is never allowed to take the watcher down with it.

### Snapshot in your push notifications

Notifications to [ntfy](https://ntfy.sh) topics include the cropped image of
the region that fired — the actual pixels, in the push. Use `ntfy://host/topic`
(TLS) or `ntfy+http://host:port/topic` (local server). Other services get
the text.

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

## License

MIT

MQTT support uses the Eclipse Paho Go client (EPL-2.0/EDL-1.0).
