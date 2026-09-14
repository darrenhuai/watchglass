# watchglass — Home Assistant Add-on

## EXPERIMENTAL

The main repository doubles as an add-on repository: `repository.yaml` at
its root, this directory as the add-on. It pulls the published
`ghcr.io/darrenhuai/watchglass` image (amd64 and aarch64; armv7 isn't
built yet). It hasn't been submitted to the official store and hasn't had
wide testing on real Supervisors, so expect rough edges and please report
them — the plain Docker/Compose route in the main README is the
better-trodden path.

## Installing

Settings → Add-ons → Add-on store → ⋮ → Repositories, paste
`https://github.com/darrenhuai/watchglass`, then install **watchglass**
and start it. Or use the one-click link:

[Add the watchglass repository to Home Assistant](https://my.home-assistant.io/redirect/supervisor_add_addon_repository/?repository_url=https%3A%2F%2Fgithub.com%2Fdarrenhuai%2Fwatchglass)

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
