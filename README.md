# watchglass

**changedetection.io for video streams.** Point it at any screen — a 3D
printer's LCD, a lab instrument, a server console — draw a region, and get a
push notification when that region's text or pixels change. Self-hosted, one
binary, nothing leaves your network.

> Early development. Core engine, web UI, RTSP/ffmpeg sources,
> MQTT/Home Assistant discovery, Docker packaging, and tagged binary
> releases all work; a published Home Assistant add-on is next.

## Install

### Docker Compose (recommended)

    git clone https://github.com/darrenhuai/watchglass && cd watchglass
    mkdir -p config
    docker compose up -d

Binds to `127.0.0.1:8080` by default — widen the port mapping in
`docker-compose.yml` deliberately if you want it reachable elsewhere on your
network (see [Authentication](#authentication) first). Config and the
SQLite history database live in `./config`, bind-mounted into the
container.

### Docker (build it yourself)

    git clone https://github.com/darrenhuai/watchglass && cd watchglass
    docker build -t watchglass .
    docker run -d --name watchglass -p 127.0.0.1:8080:8080 -v ./config:/config watchglass

### Binaries

Grab the latest [release](https://github.com/darrenhuai/watchglass/releases)
for prebuilt binaries — Linux, Windows, and macOS — no Go toolchain
required. ffmpeg and tesseract aren't bundled; install them separately if
you need RTSP sources or OCR triggers.

### From source (Go toolchain)

    git clone https://github.com/darrenhuai/watchglass && cd watchglass
    go install ./cmd/watchglass

Installs to `$(go env GOPATH)/bin` (`$HOME/go/bin` by default).

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

The broker password lives in plaintext in `config.yaml` — keep the file
private (watchglass writes it `0o600` on Unix); a secrets-manager story is
future work.

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
on your network — add an `auth:` block first (below) or put it behind a
reverse proxy. Watch `source` URLs are also fetched by the server, so
exposing the UI beyond localhost hands whoever reaches it a server-side fetch
primitive too — another reason not to expose it unauthenticated.

### Authentication

Add an `auth:` block to `config.yaml` to put the web UI behind HTTP Basic
auth:

    auth:
      username: admin
      password: change-me

The password lives in plaintext in `config.yaml` — same posture as the MQTT
broker password above (the file is written `0o600` on Unix; keep it
private). Omit the block entirely for no auth, which is fine as long as you
stay on localhost.

If `-listen` is bound to anything other than `127.0.0.1`/`localhost` and no
`auth:` block is set, watchglass logs a `WARNING` on every startup. The
Docker image always binds `0.0.0.0:8080` *inside* the container — that's
correct there, since reachability is actually decided by the container's
port mapping, not the bind address — so the container logs that warning on
every start regardless. With the compose file's default mapping
(`127.0.0.1:8080:8080`) the UI stays loopback-only anyway, making the
warning expected and harmless; add an `auth:` block before widening that
mapping to anything else.

### Behind a reverse proxy

`-base-path` mounts the UI under a URL prefix — e.g. `https://home.example.com/watchglass/`
instead of its own subdomain or port — for a proxy that **strips the
prefix** before forwarding. watchglass itself always serves at `/`; it never
routes on the prefix. All `-base-path` does is prepend it to every link,
form action, and redirect the UI writes into its own HTML, so the browser's
next request already carries the prefix the proxy is about to strip back
off. An nginx `location` block doing that strip looks like:

    location /watchglass/ {
        proxy_pass http://127.0.0.1:8080/;
    }

Note the trailing slash on both sides — that's what tells nginx to strip
`/watchglass/` before forwarding. Pair it with:

    go run ./cmd/watchglass -base-path /watchglass

`-base-path` must start with `/`; a trailing slash is trimmed automatically,
so `/watchglass` and `/watchglass/` are equivalent. Leaving it unset (the
default) is unchanged, unprefixed behavior — nothing here matters unless
you're proxying under a subpath.

Because watchglass never generates an absolute self-URL (everything it
writes is prefix-relative, per above), it has no need to read
`X-Forwarded-Host`, `X-Forwarded-Proto`, or similar headers, and none of
that needs to be configured on the proxy side. The [Authentication](#authentication)
section above still applies in front of a proxy exactly as it does standalone —
`-base-path` only changes URLs, not access control.

## History

Every reading is recorded to a SQLite database at `-db` (default
`watchglass.db`). `history_days` in `config.yaml` controls how long they're
kept — default 30, or `-1` to keep everything forever:

    history_days: 30

Pruning runs once at startup and then once every 24 hours; with `-1` it
never runs at all.

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

## Recipes

Seeded, copy-paste `watches:` configs for common gear live in
[docs/recipes](docs/recipes/): a generic snapshot camera, RTSP, a
closed-firmware printer LCD, a server console/IPMI KVM, and a seven-segment
lab instrument. Don't see your screen there — [open an
issue](https://github.com/darrenhuai/watchglass/issues) and we'll add one.

## Roadmap

Post-v1, roughly in priority order:

- [ ] Home Assistant Add-on — skeleton in `addon/` today, not yet
      installable (no published image)
- [ ] RapidOCR engine (PP-OCRv5-mobile) for hard text
- [ ] Native seven-segment decoder
- [x] Recipes gallery (community configs per device)
- [ ] Template matching triggers ("this icon appeared")
- [ ] Multi-region compound conditions ("A matches AND B > 200")
- [ ] Optional VLM engine (opt-in, off by default, clearly labeled)

## License

MIT

MQTT support uses the Eclipse Paho Go client (EPL-2.0/EDL-1.0).
