# watchglass — Home Assistant Add-on

## EXPERIMENTAL — not yet installable from the store

`config.yaml` now points at the published `ghcr.io/darrenhuai/watchglass`
image (amd64 and aarch64; armv7 isn't built yet), so the Supervisor has
something to pull. What's still missing is the add-on *repository*
packaging — a `repository.yaml` at the root of a repo with this directory
one level down — so "add repository → install" doesn't work yet. Until
that lands, run watchglass via the plain Docker/Compose route in the main
repository README.

## Once installable

Add this repository to the Supervisor's add-on store, install watchglass,
and start it.

### Config

The config directory is mounted at `/config` inside the container (via the
`addon_config:rw` map) — drop a `config.yaml` there, same format as the
standalone binary. Point each watch's `source` at a camera snapshot URL or
RTSP stream, draw a region, and pick a trigger.

### Web UI

No Home Assistant ingress — the add-on publishes port 8080 directly, so
open `http://<home-assistant-host>:8080` for the dashboard.

### Home Assistant entities via MQTT

For watches as HA entities (reading, health, motion, camera) instead of
just the dashboard, add an `mqtt:` block pointing at the Mosquitto broker
add-on:

    mqtt:
      broker: tcp://core-mosquitto:1883
      username: watchglass
      password: secret

See the main repository README's Home Assistant section for what each
watch's entities look like.
