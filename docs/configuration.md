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
| `unit`, `device_class` | none | How a `numeric` or `value` watch's number is described to Home Assistant ([home-assistant.md](home-assistant.md#units-and-device-classes)). |
| `counter` | `false` | The number only goes up, like a meter's total ([below](#counters)). Always on for the `energy`, `water`, `gas` and `volume` device classes. |
| `max_rise` | none | On a counter: the most the number may go up between two readings ([below](#rise-limit)). |

Durations are written like `30s`, `5m`, `2h` or `1h30m`.

## Triggers

| `type` | Fires when |
|---|---|
| `ocr_match` | the text read matches `pattern`, a regular expression, after it didn't |
| `ocr_changed` | the text changes to a new value and holds there |
| `numeric` | a number read from the text crosses `threshold`: above it with `op: gt`, below it with `op: lt` |
| `pixel_change` | at least `threshold` percent of the region's pixels changed since the previous poll |
| `value` | never: it reads a number and records it, for a meter or a logger ([below](#numbers-over-time)) |

The three text types fire on an edge: when the condition starts to hold, not on every poll while it holds. To fire again, the reading has to leave the condition and come back.

`pattern` is a [Go regular expression](https://pkg.go.dev/regexp/syntax). Plain words work, `(?i)` ignores case, and `a|b` matches either. For `numeric` and `value`, `pattern` is optional: without it the first number in the text is used; with a capture group, the group's text is used (`"FLOW\\s*([0-9.]+)"`).

**`confirm`** (default 3) is how many readings in a row must agree before watchglass believes them, because a filmed screen flickers and a single frame can be misread. For `ocr_match` and `numeric` it counts readings that meet the condition, so a value that wobbles while staying above the threshold still counts. For `ocr_changed` it counts identical readings. `pixel_change` doesn't use it. The Live panel shows the count as it builds ("1 of 3 readings in a row needed to fire").

Leaving takes more evidence than entering: once an `ocr_match` or `numeric` condition has been met, it clears only after twice `confirm` readings in a row that don't meet it, so a misread frame or two never sends the same alert again. A reading that says nothing counts on neither side: a `sevenseg` frame with nothing lit, or a `?` reading the watch skips (a blank line from tesseract or rapidocr does count, as the condition clearing). After 15 minutes of nothing but those (or twice the slowest poll interval, if that is longer), the watch starts fresh. A restart during that spell starts the 15 minutes over, so the watch still forgets, just later. After that, a condition that still holds when the display comes back is reported again once any cooldown ends.

**`cooldown`** delays a repeat alert rather than dropping it. A new state that holds through the cooldown still fires once, at the first reading after it ends; one that goes away before then never fires. `pixel_change` has no edge, so a change that persists fires again once per cooldown.

**Restarts don't repeat an alert.** Each watch keeps when it last fired and what it last settled on in the history database, so restarting watchglass, updating it, or pressing **Save & restart watch** doesn't send the same alert again, and a running cooldown still ends on time. What it settled on is only trusted if the watch read the screen within the last 15 minutes (or twice its slowest poll interval, if that is longer); after a longer break, a condition that holds when watchglass comes back is reported as new, once any cooldown that was running ends. An alert that was not sent (a wrong notify URL, or watchglass stopping before it went out) doesn't count: after you fix the URL and save, or on the next start, a condition that still holds is sent again. A camera that is down when watchglass restarts stays down without a second "down" alert, and the first reading after that sends the one "back up" alert. Unlike a fire, that holds even if the "down" alert didn't go out: it isn't sent again. The down state is kept by watch name, so if you point a down watch at another camera in `config.yaml` while watchglass is off, the watch shows the old camera's error until its first reading, and that reading sends a "back up" alert.

A watch starts fresh, and fires once if its condition already holds, when you change what it looks at or looks for: `source` (a new camera password in the URL doesn't count), `region`, `engine` and `preprocess` (`pixel_change` reads neither), or the trigger's `type`, and its `pattern`, `op` or `threshold` where the type uses them. Changing `notify`, `interval`, `confirm`, `cooldown`, `unit`, `counter`, `max_rise` or anything else keeps what it knew. Renaming a watch makes it a new one, and deleting a watch deletes what it knew.

A new watch on the UI shows four presets above the trigger fields, which fill them in without saving: **Status text** (`ocr_match` on `(?i)complete|done|error`), **Digit display** (`sevenseg` and `numeric`), **Any change** (`pixel_change` at 20%) and **Meter or logger** (`sevenseg` and `value`).

## Numbers over time

Most screens worth reading show a number you want to keep, not a moment to be told about: a meter's total, a tank level, an instrument's reading. A `value` watch reads that number and records it. It has no `op` or `threshold` and never sends an alert (a camera going down still does, if it has `notify` URLs).

```yaml
watches:
  - name: house-meter
    source: http://192.168.1.70/snapshot.jpg
    interval: 30s
    region: {x: 0.39, y: 0.13, w: 0.19, h: 0.07}
    engine: sevenseg
    unit: kWh
    device_class: energy
    max_rise: 5
    trigger:
      type: value
      confirm: 3
```

The number goes to Home Assistant's Value sensor, into the history with each reading, and out again as a CSV file ([below](#readings-out-as-csv)). It settles the way a `numeric` watch's Value does: it is the middle of the last 2 × `confirm` − 1 numbers read, so a misread frame or two never shows. A reading with a `?` in it, or with nothing read, publishes nothing. **Test this region** shows the number the watch would record.

### Counters

A counter is a number that only goes up: kWh, m³ of water or gas, litres through a pump. Set `counter: true`, or give the watch the `energy`, `water`, `gas` or `volume` device class, which are always counters. Home Assistant then gets the Value as a running total (`total_increasing`), which is what its Energy dashboard reads ([home-assistant.md](home-assistant.md#meters-and-the-energy-dashboard)).

A counter never goes down. A value lower than the last one published is held back: it isn't published and isn't recorded as the value, the log says so once, and the Live panel says "Held back: 1234.5 kWh is lower than the last value 1240.1 kWh". When the display shows the last value or more again, the watch carries on. If the meter really was replaced or reset, press **Accept** under the held value on the watch page: the lower value becomes the counter's new start, and the readings from it up are published. The accept is saved at once, so a restart before the next reading keeps it. The last value is kept in the history database too, so a lower reading just after a restart is still held back.

### Rise limit

`max_rise`, in the watch's unit, is the most a counter may go up from one published value to the next. A bigger rise is held until 2 × `confirm` − 1 readings in a row agree on it (at least 2 readings): 5 with the default `confirm` of 3. A reading agrees when it is within `max_rise` of the one before, or still climbing at the pace the meter has been keeping. A top digit misread for one or two frames (an 8 for a 3 is thousands of kWh) drops back on the next reading and never reaches Home Assistant, and neither do two different misreads in a row. A real jump, after the camera was off for a day, still gets through. Set it well above the most the meter can move between two polls, counting a backed-off interval: for a house polled every 30 s, `max_rise: 5` kWh is plenty. Set too low, a meter that climbs faster than `max_rise` at a steady pace reaches Home Assistant a few readings late, and one whose rise between polls swings well past the limit (3, then 15, then 3 with `max_rise: 5`) may not get through at all until it slows down. If the watch page keeps showing a rise held back while the meter really is climbing, raise `max_rise`.

On a `numeric` watch that is a counter, a reading the counter holds back doesn't count toward the alert either, so an alert never fires on a number the Live panel calls a misread. Until a lower value is accepted, the alert waits too.

If a wrong high value did get through (no `max_rise`, or a misread that lasted longer than the readings it needs), the counter then holds back every right reading as lower; [home-assistant.md](home-assistant.md#meters-and-the-energy-dashboard) says how to put it right. Give every meter a `max_rise`.

### Readings out as CSV

Every watch's page links **Download readings (CSV)**, which is `/watch/<name>/history.csv`. Each row is one reading: `time` (ISO 8601 with the zone, `2026-10-28T14:03:05+01:00`), `reading` (the text read), `value` (the number published with it, empty when there was none) and `fired`. Add `?from=` and `?to=` with a date (`2026-10-01`) or a time (`2026-10-01T08:00:00+02:00`) for a range; `to` takes in the whole of a date. It is behind the same login as the pages, works under `-base-path` and through the add-on, and is written out as it is read, so a long history doesn't fill memory. If the history can't be read partway through, the download fails rather than stopping short as if that were all. A reading that starts with `=`, `+`, `-` or `@` gets a `'` in front, so a spreadsheet shows it as text instead of running it as a formula; a plain number such as `-12.5` is left as it is.

```bash
curl -u admin:<PASSWORD> -o meter.csv "http://watchglass.local:8080/watch/house-meter/history.csv?from=2026-10-01"
```

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

`rotate` runs first, then `upscale`, then `grayscale`, `invert` and `threshold`.

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

It's a geometry decoder, not a font model. It works out the polarity itself, so lit LEDs on a dark face and dark LCD digits on a light one both read without any `preprocess`. It finds each digit's bars, checks the seven segment positions, and returns the digits joined: `23.5`, `-8.0`, `1234`, and `1:23` for a clock or timer. A glyph whose bars spell no digit (a letter, a half-lit segment) comes back as `?`, so `E4` on a boiler reads `?4`. So does a digit read below 40% confidence that could be another digit: a 0 whose middle bar is nearly lit might be an 8, and the reading says `?` rather than guess. A `numeric` watch skips every reading with a `?` in it, since the number around it may be wrong (`?5.5` may be 15.5): the reading doesn't count towards `confirm`, doesn't change the watch's state and doesn't reach Home Assistant. A frame with no digits on it at all (a display caught dark between refreshes, or behind a glare) is skipped the same way, so it doesn't start the count over. An `ocr_match` pattern sees the `?` as text and can match it on purpose; a reading with a `?` that the pattern doesn't match is skipped like a blank frame. The `?` of a digit read below 40% never matches, though: that reading is skipped too, so a 52 with its 5 half lit by glare, read `?2`, isn't taken for a fault code. `ocr_changed` takes a reading with a `?` that fits the last stable text (same length, every digit it did read the same: `?2.5` after `22.5`) as that text read partly, not as a change; one that doesn't fit (`?` after `0:04`, or a blank frame) is still a change, so a washer's End and a display that went dark still fire. The other way round, a whole reading that fits a partly read text the watch settled on (`22.5` after `?2.5`) takes its place without firing, so a change the camera first caught partly fires once, not again when it reads whole. A change that only ever reads with a `?` in the same place (`12:3?` after `12:34`) isn't reported until a frame reads it whole. A `?` reading the watch skips, or takes as its text read partly, doesn't reach Home Assistant's Reading either; one the pattern catches on purpose does.

**Test this region** shows one chip per glyph with its confidence; below 60 it's shown in red, and that's the digit to worry about. When the reading shows a `?` where the chip shows a digit, the decoder saw that digit below 40% and it could have been another one, so the watch doesn't act on it. Draw the box around the digits with a little room (leave out units and labels; some of the bezel or housing in the box is fine, and Test shows if it isn't) and keep the camera close to head-on: the decoder expects upright digits and copes with a mild slant, not a strong one, and not a display turned on its side. The digits need to be about 30 pixels tall in the camera's picture. [The lab instrument recipe](recipes/lab-instrument-seven-segment.md) covers what still trips it up.

### rapidocr

rapidocr runs the PaddleOCR PP-OCR models through the [RapidOCR](https://github.com/RapidAI/RapidOCR) Python package. watchglass doesn't link it: each read starts Python, hands it the crop and reads the text back, so the binary stays small and static and the engine stays optional. `pip install rapidocr` alone isn't enough; `onnxruntime` is what runs the models. Each read costs about 5 s on a desktop PC and more on a Pi, so give these watches an `interval` of 10 s or more.

## History

Every reading goes into a SQLite database (`-db`, default `watchglass.db`), with the number it published on a `numeric` or `value` watch; [history.csv](#readings-out-as-csv) takes them out again. `history_days` sets how long readings are kept: 30 days by default, or `-1` to keep everything. Old readings are pruned at start-up and then once a day. The same file keeps one small row per watch with what its trigger knows ([restarts](#triggers)) and a counter's last value; pruning leaves those alone, and a watch's rows go when the watch is deleted.

For sizing: a reading takes about 70 bytes with its indexes when the watch name and the text read are short (a dozen characters each; longer ones take more), so a watch polling every 2 s writes about 3 MB a day, roughly 90 MB over the default 30 days. That scales with the interval (poll half as often, half the size) and with the number of watches, which matters on an SD card.

## Saving from the web UI keeps your file

**Save** merges the change into `config.yaml` instead of rewriting it. Comments, key order, quoting, anchors, line endings and `- ` placement survive, and a save that changes nothing doesn't touch the file. What the first real change can lose: blank lines may be collapsed, end-of-line comments lose their alignment, a comment at the end of a block may shift its indentation, nested blocks in a 4-space file are re-indented, and a URL inside a flow-style list (`[...]`) comes back quoted. Deleting a watch deletes its own comments; a comment block parked above it moves to the next watch. A file that doesn't parse (a hand edit left half done) is never overwritten: Save reports the error and leaves it alone.

Save also checks what it writes: a notification URL that can't work, a unit Home Assistant won't take, or an engine that isn't installed is refused with the reason, and nothing is written. Hand edits to `config.yaml` take effect when watchglass restarts.

[examples/watchglass.yaml](../examples/watchglass.yaml) is a commented example with one watch of each kind.
