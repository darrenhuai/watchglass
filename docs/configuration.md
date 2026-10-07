# Configuration

Everything lives in one YAML file, `config.yaml` by default (`-config` picks another). You rarely need to edit it by hand: the web UI creates watches and saves every field on a watch's page. This page is the reference for when you do.

```yaml
# How many days of readings to keep (default 30; -1 keeps them forever).
history_days: 30

auth:                      # optional, see security.md
  username: admin
  password: change-me

mqtt:                      # optional, see home-assistant.md
  broker: tcp://homeassistant.local:1883
  username: watchglass
  password: <PASSWORD>

watches:
  - name: printer
    source: http://192.168.1.50/snapshot.jpg
    interval: 5s
    region: {x: 0.25, y: 0.45, w: 0.5, h: 0.1}
    trigger:
      type: ocr_match
      pattern: "(?i)print complete"
      confirm: 3
      cooldown: 30m
    notify:
      - ntfy://ntfy.sh/<TOPIC>
```

## Watch fields

| Field | Default | Meaning |
|---|---|---|
| `name` | required | Shown everywhere and used in the watch's URL, so no `/`, `?` or `#`. |
| `source` | required | Where frames come from. See [sources.md](sources.md). |
| `interval` | `5s` | Time between polls. At least `1s`. |
| `max_interval` | off | Adaptive polling: back off up to this while nothing changes ([sources.md](sources.md#adaptive-polling)). |
| `health_after` | `3` | Failed polls in a row (no frame, or the engine errored) before the watch counts as down and sends one alert. A frame that reads as blank is a normal poll. |
| `region` | required | The box to read, as fractions of the frame: `x` and `y` of the top-left corner, `w` and `h` its size, all 0 to 1. Drawing it in the UI fills these in. |
| `preprocess` | off | Adjustments before reading text (below). |
| `engine` | `tesseract` | How text is read: `tesseract`, `sevenseg` or `rapidocr` (below). |
| `trigger` | required | When the watch fires (below). |
| `notify` | none | Notification URLs, one per line. See [notifications.md](notifications.md). |
| `tls_insecure` | `false` | Accept a self-signed HTTPS certificate ([sources.md](sources.md#self-signed-certificates)). |
| `headers` | none | Extra request headers for an `http(s)` source, as `"Name: value"` strings. |
| `unit`, `device_class` | none | How a `numeric` watch's number is described to Home Assistant ([home-assistant.md](home-assistant.md#units-and-device-classes)). |

Durations are written like `30s`, `5m`, `2h` or `1h30m`.

## Triggers

| `type` | Fires when |
|---|---|
| `ocr_match` | the text read matches `pattern`, a regular expression, after it didn't |
| `ocr_changed` | the text changes to a new value and holds there |
| `numeric` | a number read from the text crosses `threshold`: above it with `op: gt`, below it with `op: lt` |
| `pixel_change` | at least `threshold` percent of the region's pixels changed since the previous poll |

The three text types fire on an edge: when the condition starts to hold, not on every poll while it holds. To fire again, the reading has to leave the condition and come back.

`pattern` is a [Go regular expression](https://pkg.go.dev/regexp/syntax). Plain words work, `(?i)` ignores case, and `a|b` matches either. For `numeric`, `pattern` is optional: without it the first number in the text is used; with a capture group, the group's text is used (`"FLOW\\s*([0-9.]+)"`).

**`confirm`** (default 3) is how many readings in a row must agree before watchglass believes them, because a filmed screen flickers and a single frame can be misread. For `ocr_match` and `numeric` it counts readings that meet the condition, so a value that wobbles while staying above the threshold still counts. For `ocr_changed` it counts identical readings. `pixel_change` doesn't use it. The Live panel shows the count as it builds ("1 of 3 readings in a row needed to fire").

**`cooldown`** delays a repeat alert rather than dropping it. A new state that holds through the cooldown still fires once, at the first reading after it ends; one that goes away before then never fires. `pixel_change` has no edge, so a change that persists fires again once per cooldown.

**Restarts don't repeat an alert.** Each watch keeps when it last fired and what it last settled on in the history database, so restarting watchglass, updating it, or pressing **Save & restart watch** doesn't send the same alert again, and a running cooldown still ends on time. What it settled on is only trusted if the watch read the screen within the last 15 minutes (or twice its slowest poll interval, if that is longer); after a longer break, a condition that holds when watchglass comes back is reported as new, once any cooldown that was running ends. An alert that was not sent (a wrong notify URL, or watchglass stopping before it went out) doesn't count: after you fix the URL and save, or on the next start, a condition that still holds is sent again.

A watch starts fresh, and fires once if its condition already holds, when you change what it looks at or looks for: `source` (a new camera password in the URL doesn't count), `region`, `engine` and `preprocess` (`pixel_change` reads neither), or the trigger's `type`, and its `pattern`, `op` or `threshold` where the type uses them. Changing `notify`, `interval`, `confirm`, `cooldown` or anything else keeps what it knew. Renaming a watch makes it a new one, and deleting a watch deletes what it knew.

A new watch on the UI shows three presets above the trigger fields, which fill them in without saving: **Status text** (`ocr_match` on `(?i)complete|done|error`), **Digit display** (`sevenseg` and `numeric`) and **Any change** (`pixel_change` at 20%).

### Picking a pixel_change threshold

A camera never sends exactly the same picture twice, and a backlight may flicker, so some pixels change even when nothing on the screen does. Draw the box, leave the screen alone and press **Test this region**. For `pixel_change` it grabs two frames, waits the watch's interval in between (kept between 1 and 3 seconds), and says how much of the region changed and whether the Threshold in the form would fire. It compares them the way the running watch compares each frame with the one before (the number the Live panel shows as "4.2% changed"), only with the Test's own two frames 1 to 3 seconds apart rather than one interval apart. With nothing moving, that number is the camera's own noise: the floor. A threshold a few times above it stays quiet; one at or below it fires on its own. Then press Test again while the screen does what you want to hear about (a light comes on, a page changes) and check that the change clears the threshold.

## Preprocess

These only affect reading text; `pixel_change` always compares the raw crop. Tune them with **Test this region**, which shows the crop after preprocessing next to what was read.

```yaml
preprocess:
  rotate: 90        # 90, 180 or 270: turn the crop this many degrees clockwise; 0 is off
  grayscale: true
  invert: false     # light text on a dark screen often reads better inverted
  threshold: 128    # 1-255: turn every pixel black or white at this level; 0 is off
  upscale: 2        # 2-4: enlarge small text; 0 or 1 is off
```

They run in that order.

### A display that is sideways

A phone on its side, an ESP32-CAM screwed in at a right angle, a meter whose LCD faces the wrong way: tesseract and `sevenseg` both need the text upright. `rotate` turns the crop a quarter or half turn before it is read: `90` when the tops of the letters point left in the picture, `270` when they point right, `180` when the display is upside down. In the UI it is **Rotate**, the first control under Preprocess. Try a value and press **Test this region**: the result shows the turned crop.

The region doesn't change. It stays a box on the picture as the camera sends it, and the page keeps showing that picture unturned; only the crop is turned. The crops in the Live panel, the picture attached to an ntfy alert and Home Assistant's snapshot are the turned crop too, so they arrive the right way up.

`rotate` takes those four values and nothing in between. A display that leans a few degrees is a different problem: see the `ffmpeg:` source trick in the [seven_segments recipe](recipes/from-seven-segments.md#setting-by-setting), or straighten the camera.

## OCR engines

| `engine` | Reads | Needs |
|---|---|---|
| `tesseract` (default) | printed text: LCD menus, console output, status lines | the tesseract program ([install.md](install.md#what-to-install-for-reading-text-and-streams)) |
| `sevenseg` | seven-segment digits: scales, meters, thermostats, appliance timers | nothing, it's built in |
| `rapidocr` | printed text tesseract struggles with: low contrast, small or stylised fonts, odd angles, several lines at once | Python with `pip install rapidocr onnxruntime`; not in the Docker image |

tesseract reads the region as **one line of text**. Draw the box around a single line; two watches on the same camera are cheap if you need two lines. rapidocr reads a block of lines, joined with spaces.

A watch that needs an engine this machine doesn't have doesn't start, and its page says what to install. A `sevenseg` watch runs on a machine with no tesseract at all.

### How `sevenseg` reads digits

It's a geometry decoder, not a font model. It works out the polarity itself, so lit LEDs on a dark face and dark LCD digits on a light one both read without any `preprocess`. It finds each digit's bars, checks the seven segment positions, and returns the digits joined: `23.5`, `-8.0`, `1234`, and `1:23` for a clock or timer. A glyph whose bars spell no digit (a letter, a half-lit segment) comes back as `?`, so `E4` on a boiler reads `?4`.

**Test this region** shows one chip per glyph with its confidence; below 60 it's shown in red, and that's the digit to worry about. Draw the box around the digits with a little room (leave out units and labels; some of the bezel or housing in the box is fine, and Test shows if it isn't) and keep the camera close to head-on: the decoder expects upright digits and copes with a mild slant, not a strong one, and not a display turned on its side. The digits need to be about 30 pixels tall in the camera's picture. [The lab instrument recipe](recipes/lab-instrument-seven-segment.md) covers what still trips it up.

### rapidocr

rapidocr runs the PaddleOCR PP-OCR models through the [RapidOCR](https://github.com/RapidAI/RapidOCR) Python package. watchglass doesn't link it: each read starts Python, hands it the crop and reads the text back, so the binary stays small and static and the engine stays optional. `pip install rapidocr` alone isn't enough; `onnxruntime` is what runs the models. Each read costs 2-4 s on a desktop and more on a Pi, so give these watches an `interval` of 10 s or more.

## History

Every reading goes into a SQLite database (`-db`, default `watchglass.db`). `history_days` sets how long readings are kept: 30 days by default, or `-1` to keep everything. Old readings are pruned at start-up and then once a day. The same file keeps one small row per watch with what its trigger knows ([restarts](#triggers)); pruning leaves those alone, and a watch's row goes when the watch is deleted.

For sizing: a reading takes about 189 bytes with its indexes, so a watch polling every 2 s writes about 8 MB a day, roughly 245 MB over the default 30 days. That scales with the interval (poll half as often, half the size) and with the number of watches, which matters on an SD card.

## Saving from the web UI keeps your file

**Save** merges the change into `config.yaml` instead of rewriting it. Comments, key order, quoting, anchors, line endings and `- ` placement survive, and a save that changes nothing doesn't touch the file. What the first real change can lose: blank lines may be collapsed, end-of-line comments lose their alignment, a comment at the end of a block may shift its indentation, nested blocks in a 4-space file are re-indented, and a URL inside a flow-style list (`[...]`) comes back quoted. Deleting a watch deletes its own comments; a comment block parked above it moves to the next watch. A file that doesn't parse (a hand edit left half done) is never overwritten: Save reports the error and leaves it alone.

Save also checks what it writes: a notification URL that can't work, a unit Home Assistant won't take, or an engine that isn't installed is refused with the reason, and nothing is written. Hand edits to `config.yaml` take effect when watchglass restarts.

[examples/config.yaml](../examples/config.yaml) is a commented example with one watch of each kind.
