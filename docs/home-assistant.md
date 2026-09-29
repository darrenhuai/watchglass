# Home Assistant

watchglass talks to Home Assistant over MQTT, using HA's [MQTT discovery](https://www.home-assistant.io/integrations/mqtt/#mqtt-discovery). Add an `mqtt:` block to `config.yaml` and every watch shows up in HA as a device, with nothing to configure on the HA side:

```yaml
mqtt:
  broker: tcp://homeassistant.local:1883
  username: watchglass
  password: <PASSWORD>
```

With the Mosquitto broker add-on, the broker is `tcp://core-mosquitto:1883` from the watchglass add-on, or `tcp://<home-assistant-host>:1883` from anywhere else; create an HA user for watchglass and use its login. Other brokers work too: `tcp://`, `ssl://`, `mqtt://`, `mqtts://` and `ws://` are accepted.

Optional keys: `client_id` (default `watchglass`), `base_topic` (default `watchglass`) and `discovery_prefix` (default `homeassistant`).

Changes to the `mqtt:` block take effect after a restart. The broker password is stored in plain text, like every password in `config.yaml`; watchglass writes the file readable only by its owner on Linux and macOS.

## The entities

Each watch becomes a device named `watchglass <watch name>` with these entities:

| Entity | Type | What it shows |
|---|---|---|
| Reading | sensor | The text read, once it has held (below). |
| Value | sensor | `numeric` watches only: the number, which HA can graph and keep statistics for. |
| Health | binary sensor (connectivity) | Online while frames are read, offline once the watch counts as down. Online from the first frame read. |
| Motion | binary sensor (motion) | On for 30 seconds each time the trigger fires. |
| Snapshot | camera | The cropped region from the last fire. |

HA puts the device name in front, so the Value of a watch called `boiler` is "watchglass boiler Value".

State is published with the retain flag, so it survives an HA restart. watchglass announces its own availability with an MQTT last-will message, so its entities go unavailable if it stops. It only publishes changes.

### Readings that don't flap

The Reading entity only changes when a reading has held for `confirm` polls in a row, so a frame misread through glare never reaches HA.

- For a `numeric` watch, the Value is the middle (median) of the last 2 × `confirm` − 1 numbers read. With `confirm: 2` or more, a misread digit never reaches the history graph; with `confirm: 1`, every number read is sent. A value that changes on every poll, like power draw, is still followed, one reading behind. The Reading is the text that number came from, or what the panel shows instead of a number (`Err`, `OFF`) once that has held. Seven-segment readings with an unreadable digit (`?`) are skipped.
- For `pixel_change`, the Reading is the change percentage. It's sent at once when it crosses the threshold in either direction, and otherwise at most once a minute, so camera noise doesn't make a new state every frame.

### Units and device classes

Give a `numeric` watch a unit and a device class and HA treats the Value as a proper measurement, with long-term statistics. They're under **Home Assistant** on a numeric watch's page, or in `config.yaml`:

```yaml
watches:
  - name: boiler
    source: http://192.168.1.60/snapshot.jpg
    region: {x: 0.3, y: 0.3, w: 0.3, h: 0.2}
    engine: sevenseg
    unit: "°C"
    device_class: temperature
    trigger:
      type: numeric
      op: lt
      threshold: 40
```

| `device_class` | Units HA accepts |
|---|---|
| `temperature` | °C, °F, K |
| `humidity` | % |
| `pressure` | hPa, kPa, Pa, mPa, bar, cbar, mbar, psi, mmHg, inHg, inH₂O |
| `power` | W, kW, mW, MW, GW, TW |
| `voltage` | V, mV, μV, kV, MV |
| `current` | A, mA, μA |
| `weight` | kg, g, mg, μg, lb, oz, st |
| `duration` | s, min, h, d, ms, μs |

A device class needs one of its units. Without a device class, the unit is free text of up to 16 characters (`rpm`, `L`). HA spells micro with the Greek μ; a `µ` typed on a keyboard is changed into it when you save. Both fields are refused on a watch that isn't `numeric`.

When a watch stops being numeric, watchglass sends an empty config for its Value entity at start-up so HA removes it. That's expected in the broker log.

## Is it connected?

With an `mqtt:` block, the top of every page says **Home Assistant: connected**, **connecting…**, or **not connected** with the reason: connection refused, wrong username or password, no answer, certificate not trusted and so on. Hover over it for the broker address and the raw error. The log says the same, at most once a minute:

```text
mqtt: can't connect to tcp://homeassistant.local:1883: connection refused (retrying): …
```

If the broker is down, watchglass keeps watching, retries at least every 15 seconds, and sends everything that changed once it's back. MQTT is never allowed to take the watcher down with it.

## The add-on

The watchglass add-on runs the same image inside Home Assistant (amd64 and aarch64). It's experimental. See [addon/DOCS.md](../addon/DOCS.md) for how to install it and what to watch out for.

Entity names changed in 0.1.8, from "watchglass printer printer reading" to "watchglass printer Reading". The entity IDs didn't change, so automations keep working.
