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
| `max_interval` | off | Adaptive polling: back off up to this while nothing changes ([sources.md](sources.md#adaptive-polling)). On a watch with a `gate`, a skipped poll counts as a change ([cycling displays](#a-display-that-cycles-through-values)). |
| `health_after` | `3` | Failed polls in a row (no frame, or the engine errored) before the watch counts as down and sends one alert. A frame that reads as blank is a normal poll. |
| `region` | required | The box to read, as fractions of the frame: `x` and `y` of the top-left corner, `w` and `h` its size, all 0 to 1. Drawing it in the UI fills these in. |
| `preprocess` | off | Adjustments before reading text (below). |
| `engine` | `tesseract` | How text is read: `tesseract`, `sevenseg` or `rapidocr` (below). |
| `gate` | none | A second box, for a display that cycles through values: the value is read only on frames where the gate's code matches ([below](#a-display-that-cycles-through-values)). |
| `letters` | `false` | `sevenseg` only: read the letters of fault codes and status words (`E4`, `End`, `Err`) instead of `?` ([the letter table](#letters-on-a-seven-segment-display)). |
| `trigger` | required | When the watch fires (below). |
| `notify` | none | Notification URLs, one per line. See [notifications.md](notifications.md). |
| `tls_insecure` | `false` | Accept a self-signed HTTPS certificate ([sources.md](sources.md#self-signed-certificates)). |
| `headers` | none | Extra request headers for an `http(s)` source, as `"Name: value"` strings. |
| `unit`, `device_class` | none | How a `numeric` or `value` watch's number is described to Home Assistant ([home-assistant.md](home-assistant.md#units-and-device-classes)). |
| `counter` | `false` | The number only goes up, like a meter's total ([below](#counters)). Always on for the `energy`, `water`, `gas` and `volume` device classes. |
| `max_rise` | none | On a counter: the most the number may go up between two readings ([below](#rise-limit)). |
| `check_moves` | `true` | Notice when the picture around the box moves ([below](#when-the-camera-or-the-screen-moves)). `false` turns it off. |
| `move_alert` | `false` | Also send one alert to the `notify` URLs when it does ([below](#when-the-camera-or-the-screen-moves)). |

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

A watch starts fresh, and fires once if its condition already holds, when you change what it looks at or looks for: `source` (a new camera password in the URL doesn't count), `region`, `gate`, `engine` and `preprocess` (`pixel_change` reads neither), or the trigger's `type`, and its `pattern`, `op` or `threshold` where the type uses them. Changing `notify`, `interval`, `confirm`, `cooldown`, `unit`, `counter`, `max_rise` or anything else keeps what it knew. Renaming a watch makes it a new one, and deleting a watch deletes what it knew.

A new watch on the UI shows four presets above the trigger fields, which fill them in without saving: **Status text** (`ocr_match` on `(?i)complete|done|error`), **Digit display** (`sevenseg` and `numeric`), **Any change** (`pixel_change` at 20%) and **Meter or logger** (`sevenseg` and `value`).

### Picking a pixel_change threshold

A camera never sends exactly the same picture twice. Sensor noise, JPEG compression, a flickering backlight and a mount that shakes all change some pixels when nothing on the screen does. The threshold has to sit above that noise and below the change you want to hear about, so measure both on your own camera:

1. Draw the box and press **Test this region** with the screen left alone. For `pixel_change` it grabs two frames 1 to 3 seconds apart (the watch's interval, kept in that range) and says how much of the region changed, compared the way the running watch compares each frame with the one before. That's a first look at the noise, from one pair of frames.
2. Save the watch with Threshold `100` and its notify list empty while you measure, and leave the screen alone. At 100, noise alone won't fire it, but a frame where every pixel in the box changes at once (the lights go out, the camera restarts or changes its picture size) still fires, and with nothing to notify that sends no alert. Each tile in the Live panel says how much changed since the frame before ("4.2% changed"), and the panel keeps the last 10 readings. Look at it now and then over a few minutes, or longer if the room's light changes (lamps, daylight, a camera that switches to night vision), and note the highest number you see. That's your camera's noise.
3. Press Test while the screen does what you want to hear about (a light comes on, a page changes, a number moves on), or watch the Live panel as it happens. That's the smallest real change.
4. Set the threshold between the two: above the highest noise you saw, and below the real change. Then put the watch's notify targets back. If the real change isn't clearly bigger than the noise, no threshold will work; see below.

`confirm` doesn't apply to `pixel_change`: one pair of frames over the threshold fires. `cooldown` stops a run of noisy frames from sending an alert each, but it also hides a real change that comes during it.

On an emulated camera with a shake of 1 to 3 pixels, a flickering backlight, passing hands and an IR switch, the frames with nothing changing moved 9% of the box half the time, 22 to 29% once in a hundred frames and up to 99% when a hand passed or the IR light switched. The real change, a counter going from 984 to 1284, moved only 6 to 22%. A threshold of 5% sent 112 false alerts an hour, and with a 30 s cooldown only 4 of 30 real changes got their own alert. 40% sent 8 false alerts and missed all 30 real changes. The same camera held to a shake of at most 1 pixel, with no flicker, moved at most 4.4% when still, and the change always moved more than 5%, so 5% separated them.

So a camera that shakes by more than a pixel, or whose picture flickers (auto exposure hunting, an IR light switching on and off, a backlight beating with the camera's frame rate), may have no threshold that works. Then:

- Mount the camera so it can't move: a bracket, not a shelf that a door or a fan shakes.
- Steady the light: fixed exposure if the camera has it, IR or night mode off, a lamp that stays on.
- Draw a smaller box around only the part that changes (a status light rather than the whole panel), so the change is a bigger share of the box.
- Use a text watch if what changes is words or digits. `ocr_changed` or `ocr_match` with `confirm` ignores a picture that moves while the text stays the same.

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

### A display that cycles through values

Many meters and heat pumps show one number at a time and step through several: the import total, the export total, tariff 1, tariff 2, each for a few seconds with a code beside it (`1.8.0`, `2.8.0`) and often a blank frame between. The numbers look alike, so a pattern can't tell them apart, and two watches would read two different frames. A `gate` is a second box on the same watch, around the code:

```yaml
watches:
  - name: meter-import
    source: http://192.168.1.70/snapshot.jpg
    interval: 2s
    engine: sevenseg
    region: {x: 0.31, y: 0.35, w: 0.56, h: 0.22}
    gate:
      region: {x: 0.15, y: 0.35, w: 0.15, h: 0.12}
      pattern: '^1\.?8\.?0$'
    unit: kWh
    device_class: energy
    max_rise: 5
    trigger:
      type: value
      confirm: 2
```

Every poll reads the gate first, from the same frame as the value. When the gate's reading matches its `pattern`, the value box is read and used as it would be without a gate. When it doesn't (another phase, a blank frame, a flash), the poll is skipped:

- Nothing is published to Home Assistant, settled or recorded in the history. The history has no row for a skipped poll, so `history.csv` holds only the gated phase's readings. The one exception is the watch's health in Home Assistant: a skipped poll still shows it online, so a gate that has never opened doesn't leave that sensor unknown.
- It counts toward neither side of `confirm` or the leave rule: three readings above a threshold with other phases in between are still three in a row.
- It isn't a failed poll. The camera answered and the gate was read, so a gate that never opens doesn't take the watch down, the same as a blank display. Instead the Live panel says how long the gate has been closed and the last code it read (a blank frame between phases doesn't count, nor the `?` the seven-segment decoder reads on one), and once it has been closed for `health_after` polls and 5 minutes it says so plainly ("Gate closed for 12 min (last saw '2.8.0')"), the watch list tags it, and the log says it once. The watch list also says "gate closed" beside **No readings yet** for a watch whose gate hasn't opened since it started, which is what a gate box off the code or a pattern that never matches looks like.
- A long closed spell is treated like a long blank one. Once nothing has been read for 15 minutes (or twice the slowest poll interval, if that is longer), the watch forgets what it had settled on, so a condition that still holds when the gate opens again is reported again, subject to `cooldown`. The normal cycle, with the phase you want every few seconds to a minute, never gets near that.

A gate that the engine fails to read is a failed read, like a value it fails to read.

`gate` takes:

| Field | Default | Meaning |
|---|---|---|
| `region` | required | The box around the code, as fractions of the frame like `region`. |
| `pattern` | required | A regular expression the gate's reading must match. Anchor it with `^` and `$` when one code is part of another (`2` and `12`). The seven-segment decoder can miss a small dot and read `1.8.0` as `1.80` or `180`, so make the dots optional: `'^1\.?8\.?0$'`. A seven-segment digit the decoder wasn't sure of never opens the gate. |
| `engine` | the watch's | How the code is read, when it is printed differently from the value. Every poll reads the gate, so a slow engine makes every poll slow: rapidocr starts Python for each read and takes seconds, long enough to miss a phase that shows for a few seconds. Use `sevenseg` or `tesseract` when either reads the code. |
| `preprocess` | the watch's | The code's own preprocessing, written like `preprocess`. |

Pick a short `interval`: the watch only sees the phase you want when a poll lands on it. With phases a few seconds long, poll every 1 or 2 seconds. `max_interval` works on a gated watch, but a skipped poll counts as a change and puts it back on `interval`: a gated watch only backs off while the gate stays open on the same value, so polls that slowed down can't fall out of step with the cycle and miss the phase for minutes. In practice a display that keeps cycling polls at `interval`. On the watch page, tick **Use a gate**, draw the gate box (it is dashed and blue; **Value box** and **Gate box** under the image, or a click inside a box, pick which one you are moving; a drag always draws the one picked), and press **Test this region**: it reads both boxes from one frame and says whether the gate was open. Changing the gate starts the watch fresh, like changing its region. The [cycling meter recipe](recipes/meter-cycling-obis.md) has a tested example.

### Readings out as CSV

Every watch's page links **Download readings (CSV)**, which is `/watch/<name>/history.csv`. Each row is one reading: `time` (ISO 8601 with the zone, `2026-10-28T14:03:05+01:00`), `reading` (the text read), `value` (the number published with it, empty when there was none) and `fired`. Add `?from=` and `?to=` with a date (`2026-10-01`) or a time (`2026-10-01T08:00:00+02:00`) for a range; `to` takes in the whole of a date. It is behind the same login as the pages, works under `-base-path` and through the add-on, and is written out as it is read, so a long history doesn't fill memory. If the history can't be read partway through, the download fails rather than stopping short as if that were all. A reading that starts with `=`, `+`, `-` or `@` gets a `'` in front, so a spreadsheet shows it as text instead of running it as a formula; a plain number such as `-12.5` is left as it is.

```bash
curl -u admin:<PASSWORD> -o meter.csv "http://watchglass.local:8080/watch/house-meter/history.csv?from=2026-10-01"
```

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

For `sevenseg`, leave `threshold` (**Binarize** in the UI) off on a reflective LCD. One threshold for the whole box loses the digits at the dim end when the light is uneven, and the decoder picks its own levels and evens out the light. On 28 photos of reflective LCDs, 16 read right with it off; across five Binarize levels, 17 of 140 tries did.

### A display that is sideways

A phone on its side, an ESP32-CAM screwed in at a right angle, a meter whose LCD faces the wrong way: tesseract, `sevenseg` and rapidocr all need the text upright. `rotate` turns the crop a quarter or half turn before it is read: `90` when the tops of the letters point left in the picture, `270` when they point right, `180` when the display is upside down. In the UI it is **Rotate**, the first control under Preprocess. Try a value and press **Test this region**: the result shows the turned crop.

The region doesn't change. It stays a box on the picture as the camera sends it, and the page keeps showing that picture unturned; only the crop is turned. The crops in the Live panel, the picture attached to an ntfy alert and Home Assistant's snapshot are the turned crop too, so they arrive the right way up.

`rotate` takes those four values and nothing in between. A display that leans a few degrees is a different problem: see the `ffmpeg:` source trick in the [seven_segments recipe](recipes/from-seven-segments.md#setting-by-setting), or straighten the camera.

## OCR engines

| `engine` | Reads | Needs |
|---|---|---|
| `tesseract` (default) | printed text: LCD menus, console output, status lines | the tesseract program ([install.md](install.md#what-to-install-for-reading-text-and-streams)) |
| `sevenseg` | seven-segment digits: scales, meters, thermostats, appliance timers | nothing, it's built in |
| `rapidocr` | printed text tesseract struggles with: low contrast, small or stylised fonts, odd angles, several lines at once; sometimes a [digit display `sevenseg` can't read](#rapidocr-on-digit-displays) | Python with `pip install rapidocr onnxruntime`; not in the Docker image |

tesseract reads the region as **one line of text**. Draw the box around a single line; two watches on the same camera are cheap if you need two lines. rapidocr reads a block of lines, joined with spaces.

A watch that needs an engine this machine doesn't have doesn't start, and its page says what to install. A `sevenseg` watch runs on a machine with no tesseract at all.

### How `sevenseg` reads digits

It's a geometry decoder, not a font model. It works out the polarity itself, so lit LEDs on a dark face and dark LCD digits on a light one both read without any `preprocess`. It finds each digit's bars, checks the seven segment positions, and returns the digits joined: `23.5`, `-8.0`, `1234`, and `1:23` for a clock or timer. A colon reads whether its dots sit near the middle or level with the top and bottom bars, and when they lean with italic digits. Digits after the point drawn smaller than the rest, as on many thermometers, read too, and so do a clock's smaller seconds. On a reflective LCD lit unevenly (a lamp to one side, a shadow across one end), a digit that doesn't read at first is read again with the light evened out. Evening out the light can make faint unlit segments look lit, so that second reading is dropped when it lights segments the first one saw unlit; on an LCD where a shadow's edge falls across the digits it can still read a wrong digit, so check such a display with Test this region. A bar standing alone at the edge of the box (the window's frame, a reflection) reads `?`, not `1`. When the last digits stand apart from the rest and come out paler, as when a lamp lights one side of the display, the decoder reads them again on their own; if it still can't, the reading ends in `?` rather than being a shorter number. A digit the box cuts through at either end usually reads `?` the same way, but one left wholly outside the box can't be noticed, and a sliver of one may be dropped, so leave a margin of about half a digit around the number. A display showing only a `1` needs a box drawn close round it: with nothing else in the box, a lone `1` looks like the edge of the window and reads `?`. A glyph whose bars spell no digit (a letter, a half-lit segment) comes back as `?`, so `E4` on a boiler reads `?4` unless the watch has `letters: true` (below). So does a digit read below 40% confidence that could be another digit: a 0 whose middle bar is nearly lit might be an 8, and the reading says `?` rather than guess. A `numeric` watch skips every reading with a `?` in it, since the number around it may be wrong (`?5.5` may be 15.5): the reading doesn't count towards `confirm`, doesn't change the watch's state and doesn't reach Home Assistant. A frame with no digits on it at all (a display caught dark between refreshes, or behind a glare) is skipped the same way, so it doesn't start the count over. An `ocr_match` pattern sees the `?` as text and can match it on purpose; a reading with a `?` that the pattern doesn't match is skipped like a blank frame. The `?` of a digit read below 40% never matches, though: that reading is skipped too, so a 52 with its 5 half lit by glare, read `?2`, isn't taken for a fault code. `ocr_changed` takes a reading with a `?` that fits the last stable text (same length, every digit it did read the same: `?2.5` after `22.5`) as that text read partly, not as a change; one that doesn't fit (`?` after `0:04`, or a blank frame) is still a change, so a washer's End and a display that went dark still fire. The other way round, a whole reading that fits a partly read text the watch settled on (`22.5` after `?2.5`) takes its place without firing, so a change the camera first caught partly fires once, not again when it reads whole. A change that only ever reads with a `?` in the same place (`12:3?` after `12:34`) isn't reported until a frame reads it whole. A `?` reading the watch skips, or takes as its text read partly, doesn't reach Home Assistant's Reading either; one the pattern catches on purpose does.

### Letters on a seven-segment display

A boiler shows `E4`, a washer `End`, a meter `OL` (which reads `0L`, below). With `letters: true` on a `sevenseg` watch (**Letters** on the watch's page, shown while Engine is sevenseg) the decoder reads the letters a seven-segment display can draw, in the case the displays draw them:

| Letter | Lit bars | Letter | Lit bars |
|---|---|---|---|
| `A` | a b c e f g | `n` | c e g |
| `b` | c d e f g | `o` | c d e g |
| `C` | a d e f | `P` | a b e f g |
| `c` | d e g | `r` | e g |
| `d` | b c d e g | `t` | d e f g |
| `E` | a d e f g | `U` | b c d e f |
| `F` | a e f g | `u` | c d e |
| `H` | b c e f g | `y` | b c d f g |
| `h` | c e f g | `-` | g |
| `J` | b c d e | | |
| `L` | d e f | | |

(a is the top bar, b and c the right-hand bars from the top, d the bottom, e and f the left-hand bars from the bottom, g the middle.) A letter whose bars are a digit's reads as the digit: `O` is `0`, `S` is `5`, `I` is `1`, `Z` is `2`, `g` is `9` and `B` is `8`, so write `L0` in a pattern for a display that shows `LO`. `b` is a `6` without its top bar, the bar a glancing light most often leaves too faint to see, and some displays draw their 6 that way, so the digit wins: it reads `6`, as with letters off, and `b` only when another letter stands in the row (`bE`, `Prob`). `y` and `U` are a `9` and a `0` without the top bar, so they are read only when two of the decoder's thresholds agree on them, or when another letter stands in the row; on a crop with `threshold` set, which has one level, they read `?` unless another letter is there. On some fonts an `L` reads as `1` with a point after it, because its foot looks like a decimal point (`LO` can read `1.0`, with letters on or off), so check a display that shows `L` with Test this region before a `numeric` watch relies on it. A row of nothing but dashes, two or more, reads as them (`--`, a display standing by); a single dash alone reads `?`, since a digit eroded to its middle bar looks the same.

Letters are read more carefully than digits, because a digit with a bar missed looks like a letter (a 0 cut along the top is a `U`, a 6 cut on the right an `E`): a letter reads only at 40% confidence or more, only when no other threshold the decoder tries reads a digit there, only from a glyph as tall as the digits (or a lower-case one in the lower half of the row), never on the edge of the box or against the housing or the window's rim, where a bar may have been lost, and never touching a piece of ink that spelled nothing (a `?` or a sliver), which is the rest of a digit that came apart; otherwise it is `?`. The lower-case letters `c`, `n`, `o`, `r` and `u` stand in the lower half of the row and read only within a digit's width of a capital letter, or of another lower-case letter that is (`End`, `Err`, `nCr`, `oPEn`); alone, or beside digits only, they don't, because the lower half of a digit too pale for the decoder looks like a `u`. A tenths digit drawn smaller after the point, and a clock's smaller seconds, stay digits. Off (the default), every watch reads exactly as before this setting existed.

A letter is never part of a number. A `numeric` watch, and the Value it sends to Home Assistant, treat a reading with a letter in it exactly like one with a `?`: `E4` is never 4 and `2b.5` never 2 or .5; the reading is skipped, counts towards `confirm` on neither side and changes nothing. `ocr_match` and `ocr_changed` see the letters as text, so `^E[0-9]+$` catches a boiler's fault codes and `^End$` a washer's end screen (the [boiler](recipes/heat-pump-boiler-panel.md) and [appliance](recipes/appliance-time-remaining.md) recipes). That is better than matching a `?`, which a hand over the display or a glare produces too. Test this region shows each letter as its own chip with its confidence.

**Test this region** shows one chip per glyph with its confidence; below 60 it's shown in red, and that's the digit to worry about. When the reading shows a `?` where the chip shows a digit, the decoder saw that digit below 40% and it could have been another one, so the watch doesn't act on it. Draw the box around the digits with a little room (leave out units and labels; some of the bezel or housing in the box is fine, and Test shows if it isn't) and keep the camera close to head-on: the decoder expects upright digits and copes with a mild slant, not a strong one, and not a display turned on its side. The digits need to be about 30 pixels tall in the camera's picture. Smaller digits are enlarged before they are read: on clean test images digits down to 10 pixels tall read, but of six real photos we tried with digits under 30 pixels, two did. Under about 15 pixels a loose box can give a wrong number, not only a `?` (`1:05` read as `105` in our tests), so for digits that small draw the box close and check the reading with Test before you trust it. Leave `upscale` off for this engine. The decoder's own enlarging is smoother, and on the 75 photos of seven-segment displays we test with, `upscale: 2` read fewer of them right (46 against 52, and 11 against 16 of the 28 reflective LCDs). [The lab instrument recipe](recipes/lab-instrument-seven-segment.md) covers what still trips it up.

### rapidocr

rapidocr runs the PaddleOCR PP-OCR models through the [RapidOCR](https://github.com/RapidAI/RapidOCR) Python package. watchglass doesn't link it: each read starts Python, hands it the crop and reads the text back, so the binary stays small and static and the engine stays optional. `pip install rapidocr` alone isn't enough; `onnxruntime` is what runs the models. Each read takes about 4 s on a desktop PC, up to 6, and more on a Pi, so give these watches an `interval` of 10 s or more.

watchglass runs rapidocr with its text-direction check off. That check is meant for photographed pages that may be upside down, and it turns digit displays over: with it on, 14 of the 73 digit displays below had some or all of their digits read upside down, with full confidence (`12:05` as `50:21`, `8.34` as `hE8`). With it off, rapidocr no longer turns text that really is upside down. If the display is upside down in the camera's picture (a camera mounted the wrong way up), set `rotate: 180` ([a display that is sideways](#a-display-that-is-sideways)); Test this region shows the turned crop. A rapidocr watch on such a display that read fine on 0.1.12 or earlier needs it now.

#### rapidocr on digit displays

`sevenseg` is the engine for seven-segment digits, and rapidocr is worth a try only on a display `sevenseg` can't read. On 73 photos of real digit displays (clocks, meters, thermostats, pumps, VFDs, scales):

| | read exactly | confidently wrong | unsure or nothing |
|---|---|---|---|
| `sevenseg` | 37 | 2 | 34 |
| rapidocr | 46 | 13 | 14 |

- On the 34 displays `sevenseg` couldn't read, rapidocr read 18 exactly and gave a confident wrong reading on 5.
- On the 37 that `sevenseg` read, rapidocr got 6 wrong, so don't switch a display that already reads.
- rapidocr's wrong readings look like readings: `3 14 g` for 314 on a kitchen scale, `9.86 96 044` for 9.8696044 on a calculator. A `numeric` watch takes the first number, so it would act on 3 or 9.86.
- A rapidocr read takes seconds (above); `sevenseg` takes a few hundredths of a second.

So if Test this region gets only `?` or no digits from `sevenseg` whatever you try, switch the Engine to rapidocr and press Test on several frames, including the display's other states. Keep it only if every one reads right, and leave the watch's `confirm` at 3 or more.

## When the camera or the screen moves

A camera gets bumped, a screen slides on its stand, or the camera's switch to night vision shifts the picture. The box then sits partly off the digits, and the watch reads part of the number or the wrong one. watchglass notices, and says so on the watch's page.

**How.** After a watch's first three clean readings (text with no unsure digit, and for `sevenseg` no `?`; any frame for `pixel_change`), it keeps a small grayscale picture of the area around the box: the bezel, the housing, the labels. The box itself is left out, so digits that change don't count. Every 5 seconds it lines the current frame up with that picture by the edges in it, so a change of exposure, white balance or night vision doesn't count either, and works out how far the picture has moved. It also tries small turns, up to about 4 degrees, so a camera that was knocked a little crooked is measured at the box rather than across the whole picture.

**What it says.**

- When four checks in a row (about 20 seconds) agree that the picture moved by at least 4.5 pixels, the Live panel says "The picture around this box moved about 14 px right and 3 px down since 14:02. Readings may be wrong until the box is moved." The watch list marks the watch "camera moved", and the log has a line. A camera that shakes on its own has to move by more: about 3 times how far its picture usually jumps from one check to the next, if that is more than 4.5 pixels, and never more than 9. A picture that creeps, or gets nudged a few pixels at a time, is reported once it has gone that far in all. While the warning is shown, a further move updates the offset on the page and in the log, without a new alert.
- When nothing lines up for 5 minutes, and the box hasn't read cleanly in that time (three clean readings in a row; a `?` from the seven-segment decoder isn't a clean reading, and nor is a lone digit it makes out in the dark), the panel says the picture around the box hasn't been in view since then: the lens is covered, or the camera points somewhere else. A screen that shows something else for a while (a menu, a boot screen) while the box still reads isn't reported.
- When the picture moves back, the warning goes away by itself.

**What to do.** The warning has two buttons:

- **Move the box with it** moves the box by the measured amount, saves it to `config.yaml` (comments kept, as every save), restarts the watch and takes a new picture of the area around the box. Moving the box is a change of `region`, so the watch starts fresh, as it does for any region change ([triggers](#triggers)).
- **The new position is fine** keeps the box where it is and takes a new picture from the next frame. Press it once the camera shows what it should: a picture of a covered lens is too plain to go by, and the check then stops until the watch is saved.

Changing the box or the `source` and saving takes a new picture too.

**The alert.** With `move_alert: true`, a move (or a picture that can't be found) also sends one alert to the watch's `notify` URLs, titled `watchglass: <name> (camera moved)` or `watchglass: <name> (box not in view)`, with the same retries as other alerts. It goes out once per move: not again while the picture stays moved, and not again after a restart. It is off by default because it is a new kind of message that a watch set up before it didn't ask for.

**What it can't notice.** A move smaller than its threshold. A move bigger than the margin it keeps around the box (60% of the box's longer side), which reads as "can't find" instead. Zoom. A box with too little around it to go by: one that fills nearly the whole frame, or sits on a plain wall, gets a note on its page that the area around it is too plain or changes too much, and isn't checked until it is saved again or watchglass restarts. The same goes for a watch that never reads cleanly, since it never takes the picture. On a screen grab or a KVM, where nothing around the box stays put, set `check_moves: false`. Do the same for a box on a screen whose text scrolls around it (a console, a log, a list): when the lines move up, the check can't tell that from the camera moving, and says the picture moved by a line or more.

**Restarts.** The picture, and what the check last said, is kept in the history database, one small row per watch. A camera bumped while watchglass was off still shows as moved after the restart, and the same move isn't reported twice.

**Accuracy and cost.** On the 77 freely licensed photos of round 6's test set, moved by 5, 10 and 20 pixels, the measured offset was within 2 pixels of the true one on 460 of 462, and on 148 of 154 turned by 2 degrees. Changes of exposure (0.65 to 1.45 times) and white balance gave no shift of more than about a pixel. Two replays of the first hour of round 6's 73-minute recording of emulated cameras (12 watches polling every 1 to 2 seconds, with shake, exposure steps, night vision, hands in front, a camera reboot and a restart of watchglass) reported no move and no lost picture. The checks measured in that recording, run through the move rule in every stretch of 150 checks, forwards and backwards (316,272 checks in all), gave no move either. A check takes about 5 to 25 milliseconds on a desktop PC; most polls skip it. For those 12 watches it added about 2% of one CPU core, compared with the same build without it on the same frames. The picture it keeps takes at most 37 KB per watch. Each check makes 3 to 4 MB of short-lived garbage, so the process's memory sat about 1 MB higher most of the time, with peaks 15 to 30 MB higher before Go's garbage collector caught up.

## History

Every reading goes into a SQLite database (`-db`, default `watchglass.db`), with the number it published on a `numeric` or `value` watch; [history.csv](#readings-out-as-csv) takes them out again. `history_days` sets how long readings are kept: 30 days by default, or `-1` to keep everything. Old readings are pruned at start-up and then once a day. The same file keeps one small row per watch with what its trigger knows ([restarts](#triggers)) and a counter's last value; pruning leaves those alone, and a watch's rows go when the watch is deleted.

For sizing: a reading takes about 70 bytes with its indexes when the watch name and the text read are short (a dozen characters each; longer ones take more), so a watch polling every 2 s writes about 3 MB a day, roughly 90 MB over the default 30 days. That scales with the interval (poll half as often, half the size) and with the number of watches, which matters on an SD card.

## Saving from the web UI keeps your file

**Save** merges the change into `config.yaml` instead of rewriting it. Comments, key order, quoting, anchors, line endings and `- ` placement survive, and a save that changes nothing doesn't touch the file. What the first real change can lose: blank lines may be collapsed, end-of-line comments lose their alignment, a comment at the end of a block may shift its indentation, nested blocks in a 4-space file are re-indented, and a URL inside a flow-style list (`[...]`) comes back quoted. Deleting a watch deletes its own comments; a comment block parked above it moves to the next watch. A file that doesn't parse (a hand edit left half done) is never overwritten: Save reports the error and leaves it alone.

Save also checks what it writes: a notification URL that can't work, a unit Home Assistant won't take, or an engine that isn't installed is refused with the reason, and nothing is written. Hand edits to `config.yaml` take effect when watchglass restarts.

[examples/watchglass.yaml](../examples/watchglass.yaml) is a commented example with one watch of each kind.
